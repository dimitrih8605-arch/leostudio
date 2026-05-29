// Package server exposes the OpenAI-compatible HTTP surface backed by the
// Leonardo cookie pool service. Mirrors the core endpoints of app/main.py.
package server

import (
	"errors"
	"net/http"
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
	engine  *gin.Engine
}

// New wires up the gin engine with the core API routes.
func New(cfg config.Config, st *store.Store, svc *service.LeonardoPool) *Server {
	s := &Server{
		cfg:     cfg,
		store:   st,
		service: svc,
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
	r.GET("/health", s.handleHealth)
	r.POST("/v1/images/generations", s.handleGenerate)
	r.POST("/v1/videos/generations", s.handleGenerateVideo)
}

func (s *Server) handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// openAIImageRequest mirrors OpenAIImageRequest in main.py.
type openAIImageRequest struct {
	Prompt      string   `json:"prompt"`
	Model       string   `json:"model"`
	N           int      `json:"n"`
	Size        string   `json:"size"`
	AspectRatio string   `json:"aspect_ratio"`
	ImageURL    string   `json:"image_url"`
	ImageURLs   []string `json:"image_urls"`
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

// videoGenerationRequest is the public input for POST /v1/videos/generations.
// It mirrors the OpenAI image schema where it overlaps and adds video-specific
// fields (duration, audio, resolution).
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
