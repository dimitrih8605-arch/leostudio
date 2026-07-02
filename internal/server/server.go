// Package server exposes the full REST API surface backed by the Leonardo
// cookie pool service. The /v1/ routes are OpenAI-compatible for external
// clients; the /api/ routes expose internal management for the MCP bridge
// and desktop frontend.
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/hirotomasato/leostudio/internal/config"
	"github.com/hirotomasato/leostudio/internal/service"
	"github.com/hirotomasato/leostudio/internal/store"
)

// Server holds dependencies shared across HTTP handlers.
type Server struct {
	cfg     config.Config
	store   *store.Store
	service *service.LeonardoPool
	queue   *service.QueueManager
	engine  *gin.Engine
}

// New wires up the gin engine with the core API routes.
func New(cfg config.Config, st *store.Store, svc *service.LeonardoPool, qm *service.QueueManager) *Server {
	s := &Server{
		cfg:     cfg,
		store:   st,
		service: svc,
		queue:   qm,
	}
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery())
	s.registerRoutes(engine)
	s.engine = engine
	return s
}

// Run starts the HTTP server.
func (s *Server) Run(addr string) error {
	return s.engine.Run(addr)
}

func (s *Server) registerRoutes(r *gin.Engine) {
	// Health
	r.GET("/health", s.handleHealth)

	// OpenAI-compatible generation endpoints
	r.POST("/v1/images/generations", s.handleGenerate)
	r.POST("/v1/videos/generations", s.handleGenerateVideo)

	// --- MCP / Management API ---

	// Cookie management
	r.GET("/api/cookies", s.handleListCookies)
	r.POST("/api/cookies", s.handleAddCookie)
	r.PUT("/api/cookies/:id", s.handleUpdateCookie)
	r.DELETE("/api/cookies/:id", s.handleDeleteCookie)
	r.PATCH("/api/cookies/:id/toggle", s.handleToggleCookie)
	r.POST("/api/cookies/refresh-profiles", s.handleRefreshProfiles)
	r.POST("/api/cookies/refresh-sessions", s.handleRefreshSessions)
	r.GET("/api/cookies/health", s.handleCookieHealth)

	// Settings
	r.GET("/api/settings/:key", s.handleGetSetting)
	r.PUT("/api/settings/:key", s.handleSetSetting)

	// Image models
	r.GET("/api/models/images", s.handleListImageModels)
	r.POST("/api/models/images", s.handleAddImageModel)
	r.DELETE("/api/models/images/:id", s.handleDeleteImageModel)
	r.PUT("/api/models/images/:id/default", s.handleSetDefaultImageModel)
	r.POST("/api/models/sync", s.handleSyncImageModels)

	// Video models
	r.GET("/api/models/videos", s.handleListVideoModels)

	// Generation queue
	r.POST("/api/queue/enqueue", s.handleEnqueueJobs)
	r.GET("/api/queue", s.handleListQueueJobs)
	r.DELETE("/api/queue/:id", s.handleCancelQueueJob)
	r.POST("/api/queue/:id/retry", s.handleRetryQueueJob)
	r.DELETE("/api/queue/finished", s.handleClearFinishedJobs)

	// Generation logs / library
	r.GET("/api/logs", s.handleListLogs)

	// Aspect ratios
	r.GET("/api/aspects", s.handleListAspects)

	// Image generation (internal API)
	r.POST("/api/generate/image", s.handleGenerateInternal)

	// Video generation (internal API)
	r.POST("/api/generate/video", s.handleGenerateVideoInternal)
}

// ─── Health ──────────────────────────────────────────────────────────────────

func (s *Server) handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// ─── OpenAI-compatible image generation ──────────────────────────────────────

type openAIImageRequest struct {
	Prompt      string   `json:"prompt"`
	Model       string   `json:"model"`
	N           int      `json:"n"`
	Size        string   `json:"size"`
	AspectRatio string   `json:"aspect_ratio"`
	ImageURL    string   `json:"image_url"`
	ImageURLs   []string `json:"image_urls"`
	Style       string   `json:"style"`
}

func (s *Server) handleGenerate(c *gin.Context) {
	var payload openAIImageRequest
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(payload.Prompt) == "" {
		writeError(c, http.StatusBadRequest, "prompt is required")
		return
	}
	if payload.N == 0 {
		payload.N = 1
	}
	if payload.N < 1 || payload.N > 4 {
		writeError(c, http.StatusBadRequest, "n must be between 1 and 4")
		return
	}

	modelID, aspect, refs, err := s.resolveGenerationRequest(payload)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}

	res, err := s.service.Generate(service.GenerateRequest{
		Prompt:             payload.Prompt,
		N:                  payload.N,
		ModelID:            modelID,
		AspectRatio:        aspect,
		ReferenceImageURLs: refs,
		Style:              payload.Style,
	})
	if err != nil {
		var pe *service.PublicError
		if errors.As(err, &pe) {
			writeError(c, pe.Status, pe.Message)
			return
		}
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, res)
}

// resolveGenerationRequest mirrors _resolve_generation_request in main.py.
func (s *Server) resolveGenerationRequest(payload openAIImageRequest) (string, string, []string, error) {
	modelID := strings.TrimSpace(payload.Model)
	if modelID == "" {
		def, err := s.store.DefaultModelID()
		if err != nil {
			return "", "", nil, err
		}
		modelID = def
	}
	if modelID == "" {
		return "", "", nil, errors.New("No model configured")
	}

	// Resolve display name to UUID if needed. Accepts both UUIDs and names.
	if len(modelID) != 36 || strings.Count(modelID, "-") != 4 {
		m, err := s.store.GetModelByName(modelID)
		if err != nil {
			return "", "", nil, err
		}
		if m != nil {
			modelID = m.ModelID
		}
	}

	aspect, _ := s.store.GetSetting("default_aspect_ratio", "1:1")
	if a := strings.TrimSpace(payload.AspectRatio); a != "" {
		aspect = a
	}
	if size := strings.TrimSpace(payload.Size); size != "" {
		if mapped, ok := service.SizeAliasToAspect[size]; ok {
			aspect = mapped
		}
	}
	if !service.IsKnownAspect(aspect) {
		return "", "", nil, errors.New("aspect_ratio must be one of 16:9, 9:16, 1:1, 4:3")
	}

	var refs []string
	if u := strings.TrimSpace(payload.ImageURL); u != "" {
		refs = append(refs, u)
	}
	for _, u := range payload.ImageURLs {
		u = strings.TrimSpace(u)
		if u != "" {
			refs = append(refs, u)
		}
	}
	return modelID, aspect, refs, nil
}

func writeError(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, gin.H{
		"error": gin.H{"message": message},
	})
}

// ─── OpenAI-compatible video generation ──────────────────────────────────────

type videoGenerationRequest struct {
	Prompt      string `json:"prompt"`
	Model       string `json:"model"`
	AspectRatio string `json:"aspect_ratio"`
	Resolution  string `json:"resolution"` // 480p / 720p / 1080p
	Duration    int    `json:"duration"`   // seconds
	Audio       bool   `json:"audio"`
	ImageURL    string `json:"image_url"` // optional image-to-video start frame
}

func (s *Server) handleGenerateVideo(c *gin.Context) {
	var payload videoGenerationRequest
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(payload.Prompt) == "" {
		writeError(c, http.StatusBadRequest, "prompt is required")
		return
	}

	res, err := s.service.GenerateVideo(service.VideoRequest{
		Prompt:      payload.Prompt,
		ModelSlug:   payload.Model,
		AspectRatio: payload.AspectRatio,
		Resolution:  payload.Resolution,
		Duration:    payload.Duration,
		Audio:       payload.Audio,
		ImageURL:    payload.ImageURL,
	})
	if err != nil {
		var pe *service.PublicError
		if errors.As(err, &pe) {
			writeError(c, pe.Status, pe.Message)
			return
		}
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}

	c.JSON(http.StatusOK, res)
}

// ═══════════════════════════════════════════════════════════════════════════════
// MCP / Management API
// ═══════════════════════════════════════════════════════════════════════════════

// ─── Cookie management ───────────────────────────────────────────────────────

type cookieDTO struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	Balance   int64  `json:"balance"`
	IsActive  bool   `json:"is_active"`
	LastError string `json:"last_error,omitempty"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

func cookieToDTO(c store.Cookie) cookieDTO {
	return cookieDTO{
		ID:        c.ID,
		Email:     c.Email,
		Balance:   c.LastBalance,
		IsActive:  c.IsActive != 0,
		LastError: c.LastError,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.LastCheckedAt,
	}
}

func (s *Server) handleListCookies(c *gin.Context) {
	cookies, err := s.store.ListCookies()
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]cookieDTO, len(cookies))
	for i, ck := range cookies {
		out[i] = cookieToDTO(ck)
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) handleAddCookie(c *gin.Context) {
	var body struct {
		Value string `json:"value" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "value is required")
		return
	}
	if err := s.store.AddCookie(body.Value); err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "added"})
}

func (s *Server) handleUpdateCookie(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		Value string `json:"value" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "value is required")
		return
	}
	ok, err := s.store.UpdateCookieValue(id, body.Value)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(c, http.StatusNotFound, "cookie not found or unchanged")
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "updated"})
}

func (s *Server) handleDeleteCookie(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteCookie(id); err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (s *Server) handleToggleCookie(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		// default to toggling
		body.Enabled = true
	}
	if err := s.store.ToggleCookie(id, body.Enabled); err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "toggled", "enabled": body.Enabled})
}

func (s *Server) handleRefreshProfiles(c *gin.Context) {
	res, err := s.service.RefreshCookieProfiles()
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

func (s *Server) handleRefreshSessions(c *gin.Context) {
	res, err := s.service.RefreshCookieSessions()
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

type cookieHealthDTO struct {
	Total    int `json:"total"`
	Active   int `json:"active"`
	Inactive int `json:"inactive"`
	Depleted int `json:"depleted"`
}

func (s *Server) handleCookieHealth(c *gin.Context) {
	cookies, err := s.store.ListCookies()
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	h := cookieHealthDTO{Total: len(cookies)}
	for _, ck := range cookies {
		if ck.IsActive != 0 {
			h.Active++
		} else if ck.LastError == "DEPLETED" {
			h.Depleted++
		} else {
			h.Inactive++
		}
	}
	c.JSON(http.StatusOK, h)
}

// ─── Settings ────────────────────────────────────────────────────────────────

func (s *Server) handleGetSetting(c *gin.Context) {
	key := c.Param("key")
	fallback := c.Query("default")
	val, err := s.store.GetSetting(key, fallback)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"key": key, "value": val})
}

func (s *Server) handleSetSetting(c *gin.Context) {
	key := c.Param("key")
	var body struct {
		Value string `json:"value" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "value is required")
		return
	}
	if err := s.store.SetSetting(key, body.Value); err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "set", "key": key})
}

// ─── Image models ────────────────────────────────────────────────────────────

type modelDTO struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ModelID   string `json:"model_id"`
	SdVersion string `json:"sd_version"`
	IsDefault bool   `json:"is_default"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Server) handleListImageModels(c *gin.Context) {
	models, err := s.store.ListModels()
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]modelDTO, len(models))
	for i, m := range models {
		out[i] = modelDTO{
			ID:        m.ID,
			Name:      m.Name,
			ModelID:   m.ModelID,
			SdVersion: m.SDVersion,
			IsDefault: m.IsDefault != 0,
			CreatedAt: m.CreatedAt,
		}
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) handleAddImageModel(c *gin.Context) {
	var body struct {
		Name    string `json:"name"`
		ModelID string `json:"model_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "model_id is required")
		return
	}
	if err := s.store.AddModel(body.Name, body.ModelID); err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "added"})
}

func (s *Server) handleDeleteImageModel(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteModel(id); err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "deleted"})
}

func (s *Server) handleSetDefaultImageModel(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.SetDefaultModel(id); err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "default_set"})
}

func (s *Server) handleSyncImageModels(c *gin.Context) {
	res, err := s.service.SyncImageModels()
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// ─── Video models ────────────────────────────────────────────────────────────

func (s *Server) handleListVideoModels(c *gin.Context) {
	c.JSON(http.StatusOK, service.VideoModels)
}

// ─── Queue management ────────────────────────────────────────────────────────

type queueJobDTO struct {
	ID           int64    `json:"id"`
	Type         string   `json:"type"`
	Status       string   `json:"status"`
	Prompt       string   `json:"prompt"`
	ModelID      string   `json:"model_id"`
	AspectRatio  string   `json:"aspect_ratio"`
	Resolution   string   `json:"resolution,omitempty"`
	Duration     int      `json:"duration,omitempty"`
	Audio        int      `json:"audio,omitempty"`
	Quantity     int      `json:"quantity,omitempty"`
	RefImageIDs  []string `json:"ref_image_ids,omitempty"`
	ResultURLs   []string `json:"result_urls,omitempty"`
	ErrorMessage string   `json:"error_message,omitempty"`
	CreatedAt    int64    `json:"created_at"`
	UpdatedAt    int64    `json:"updated_at"`
}

func (s *Server) handleEnqueueJobs(c *gin.Context) {
	var specs []struct {
		Type        string   `json:"type" binding:"required"`
		Prompt      string   `json:"prompt" binding:"required"`
		ModelID     string   `json:"model_id"`
		AspectRatio string   `json:"aspect_ratio"`
		Resolution  string   `json:"resolution"`
		Duration    int      `json:"duration"`
		Audio       bool     `json:"audio"`
		Quantity    int      `json:"quantity"`
		RefImageIDs []string `json:"ref_image_ids"`
	}
	if err := c.ShouldBindJSON(&specs); err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	var jobSpecs []service.JobSpec
	for _, spec := range specs {
		qty := spec.Quantity
		if qty == 0 {
			qty = 1
		}
		jobSpecs = append(jobSpecs, service.JobSpec{
			Type:        service.JobType(spec.Type),
			Prompt:      spec.Prompt,
			ModelID:     spec.ModelID,
			AspectRatio: spec.AspectRatio,
			Resolution:  spec.Resolution,
			Duration:    spec.Duration,
			Audio:       spec.Audio,
			Quantity:    qty,
			RefImageIDs: spec.RefImageIDs,
		})
	}
	ids, err := s.queue.Enqueue(jobSpecs)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"ids": ids})
}

func (s *Server) handleListQueueJobs(c *gin.Context) {
	jobs := s.queue.List()
	out := make([]queueJobDTO, len(jobs))
	for i, j := range jobs {
		audioInt := 0
		if j.Audio {
			audioInt = 1
		}
		out[i] = queueJobDTO{
			ID:           j.ID,
			Type:         string(j.Type),
			Status:       string(j.Status),
			Prompt:       j.Prompt,
			ModelID:      j.ModelID,
			AspectRatio:  j.AspectRatio,
			Resolution:   j.Resolution,
			Duration:     j.Duration,
			Audio:        audioInt,
			Quantity:     j.Quantity,
			RefImageIDs:  j.RefImageIDs,
			ResultURLs:   j.ResultURLs,
			ErrorMessage: j.Error,
			CreatedAt:    j.CreatedAt,
			UpdatedAt:    j.UpdatedAt,
		}
	}
	c.JSON(http.StatusOK, out)
}

func (s *Server) handleCancelQueueJob(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.queue.Cancel(id); err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "canceled"})
}

func (s *Server) handleRetryQueueJob(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.queue.Retry(id); err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "requeued"})
}

func (s *Server) handleClearFinishedJobs(c *gin.Context) {
	if err := s.queue.ClearFinished(); err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "cleared"})
}

// ─── Generation logs ─────────────────────────────────────────────────────────

type logDTO struct {
	ID                   int64    `json:"id"`
	ProviderGenerationID string   `json:"provider_generation_id"`
	UsedCookieID         int64    `json:"used_cookie_id"`
	ModelID              string   `json:"model_id"`
	AspectRatio          string   `json:"aspect_ratio"`
	Prompt               string   `json:"prompt"`
	ImageURLs            []string `json:"image_urls"`
	SavedFiles           []string `json:"saved_files"`
	SaveEnabled          bool     `json:"save_enabled"`
	Status               string   `json:"status"`
	ErrorMessage         string   `json:"error_message,omitempty"`
	CreatedAt            int64    `json:"created_at"`
}

func parseJSONArray(s string) []string {
	if s == "" || s == "null" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

func (s *Server) handleListLogs(c *gin.Context) {
	limit := 100
	if l := c.Query("limit"); l != "" {
		if parsed, err := strconv.Atoi(l); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	logs, err := s.store.ListGenerationLogs(limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]logDTO, len(logs))
	for i, l := range logs {
		out[i] = logDTO{
			ID:                   l.ID,
			ProviderGenerationID: l.ProviderGenerationID,
			UsedCookieID:         l.UsedCookieID,
			ModelID:              l.ModelID,
			AspectRatio:          l.AspectRatio,
			Prompt:               l.Prompt,
			ImageURLs:            parseJSONArray(l.ImageURLsJSON),
			SavedFiles:           parseJSONArray(l.SavedFilesJSON),
			SaveEnabled:          l.SaveEnabled != 0,
			Status:               l.Status,
			ErrorMessage:         l.ErrorMessage,
			CreatedAt:            l.CreatedAt,
		}
	}
	c.JSON(http.StatusOK, out)
}

// ─── Aspect ratios ───────────────────────────────────────────────────────────

func (s *Server) handleListAspects(c *gin.Context) {
	type aspectOption struct {
		Ratio  string `json:"ratio"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
	}
	var opts []aspectOption
	for ratio, size := range service.AspectSize {
		opts = append(opts, aspectOption{Ratio: ratio, Width: size[0], Height: size[1]})
	}
	c.JSON(http.StatusOK, opts)
}

// ─── Internal image generation (same as /v1 but cleaner JSON) ────────────────

func (s *Server) handleGenerateInternal(c *gin.Context) {
	var payload openAIImageRequest
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(payload.Prompt) == "" {
		writeError(c, http.StatusBadRequest, "prompt is required")
		return
	}
	if payload.N == 0 {
		payload.N = 1
	}
	if payload.N < 1 || payload.N > 4 {
		writeError(c, http.StatusBadRequest, "n must be between 1 and 4")
		return
	}
	modelID, aspect, refs, err := s.resolveGenerationRequest(payload)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error())
		return
	}
	res, err := s.service.Generate(service.GenerateRequest{
		Prompt:             payload.Prompt,
		N:                  payload.N,
		ModelID:            modelID,
		AspectRatio:        aspect,
		ReferenceImageURLs: refs,
		Style:              payload.Style,
	})
	if err != nil {
		var pe *service.PublicError
		if errors.As(err, &pe) {
			writeError(c, pe.Status, pe.Message)
			return
		}
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}

// ─── Internal video generation ───────────────────────────────────────────────

func (s *Server) handleGenerateVideoInternal(c *gin.Context) {
	var payload videoGenerationRequest
	if err := c.ShouldBindJSON(&payload); err != nil {
		writeError(c, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(payload.Prompt) == "" {
		writeError(c, http.StatusBadRequest, "prompt is required")
		return
	}
	res, err := s.service.GenerateVideo(service.VideoRequest{
		Prompt:      payload.Prompt,
		ModelSlug:   payload.Model,
		AspectRatio: payload.AspectRatio,
		Resolution:  payload.Resolution,
		Duration:    payload.Duration,
		Audio:       payload.Audio,
		ImageURL:    payload.ImageURL,
	})
	if err != nil {
		var pe *service.PublicError
		if errors.As(err, &pe) {
			writeError(c, pe.Status, pe.Message)
			return
		}
		writeError(c, http.StatusInternalServerError, err.Error())
		return
	}
	c.JSON(http.StatusOK, res)
}
