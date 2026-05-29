// Package desktop wires the existing Leonardo pool service into a Wails app.
// Methods on App are auto-bound to the JS frontend by Wails so the React UI
// can call them like ordinary async functions.
package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/hirotomasato/leostudio/internal/leonardo"
	"github.com/hirotomasato/leostudio/internal/service"
	"github.com/hirotomasato/leostudio/internal/store"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// App is the root Wails binding. All exported methods are exposed to JS.
type App struct {
	ctx     context.Context
	store   *store.Store
	service *service.LeonardoPool
}

// NewApp constructs the app, opening the SQLite store and bootstrapping defaults.
// It panics on fatal init errors so Wails fails fast at startup.
func NewApp() *App {
	dataDir := defaultDataDir()
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		panic(fmt.Errorf("desktop: create data dir: %w", err))
	}

	dbPath := filepath.Join(dataDir, "app.db")
	st, err := store.Open(dbPath)
	if err != nil {
		panic(fmt.Errorf("desktop: open store: %w", err))
	}

	// Bootstrap seeds default settings + admin user. We deliberately do not
	// load model_id.txt because the desktop app fetches official models
	// from Leonardo on first run via the auto-sync below.
	if err := st.Bootstrap(""); err != nil {
		panic(fmt.Errorf("desktop: bootstrap store: %w", err))
	}

	client := leonardo.New()
	svc := service.NewLeonardoPool(st, client)

	return &App{
		store:   st,
		service: svc,
	}
}

// Startup is called by Wails when the app is ready. It captures the runtime
// context which we'll later use for events and window controls.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
	// Best-effort first-run sync: if the user already added cookies but the
	// models table is empty, populate it from Leonardo so Generate Image is
	// usable immediately. Failures stay silent — the Models tab has a manual
	// button anyway.
	go func() {
		models, err := a.store.ListModels()
		if err != nil || len(models) > 0 {
			return
		}
		active, err := a.store.ListActiveCookies()
		if err != nil || len(active) == 0 {
			return
		}
		_, _ = a.service.SyncImageModels()
	}()
}

// Shutdown closes the database when the window is closed.
func (a *App) Shutdown(_ context.Context) {
	if a.store != nil {
		_ = a.store.Close()
	}
}

// ----- Health / smoke test -------------------------------------------------

// Ping is a quick smoke test from the frontend to verify bindings work.
func (a *App) Ping() string {
	return "leostudio desktop bindings ok"
}

// AppInfoDTO carries metadata shown in the About dialog.
type AppInfoDTO struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Author     string `json:"author"`
	Repository string `json:"repository"`
	License    string `json:"license"`
}

// AppInfo returns static metadata about the desktop build.
func (a *App) AppInfo() AppInfoDTO {
	return AppInfoDTO{
		Name:       "LeoStudio",
		Version:    "0.1.0-dev",
		Author:     "hirotomasato",
		Repository: "https://github.com/hirotomasato/leostudio",
		License:    "MIT",
	}
}

// OpenURL opens an arbitrary URL in the user's default browser.
func (a *App) OpenURL(url string) error {
	if a.ctx == nil {
		return fmt.Errorf("desktop: app not ready")
	}
	wailsruntime.BrowserOpenURL(a.ctx, url)
	return nil
}

// ----- Cookie pool ---------------------------------------------------------

// CookieDTO is a JSON-friendly view of a stored cookie. We avoid exposing the
// raw cookie value to the frontend (security + payload size), surfacing only
// the metadata operators need.
type CookieDTO struct {
	ID             int64  `json:"id"`
	Email          string `json:"email"`
	IsActive       bool   `json:"is_active"`
	LastBalance    int64  `json:"last_balance"`
	LastError      string `json:"last_error"`
	LastUsedAt     int64  `json:"last_used_at"`
	LastCheckedAt  int64  `json:"last_checked_at"`
	DisabledReason string `json:"disabled_reason"`
	DisabledAt     int64  `json:"disabled_at"`
	CreatedAt      int64  `json:"created_at"`
	Status         string `json:"status"` // READY | DEPLETED | DISABLED
}

func cookieToDTO(c store.Cookie) CookieDTO {
	status := "DISABLED"
	if c.IsActive == 1 {
		if c.LastBalance > 0 {
			status = "READY"
		} else {
			status = "DEPLETED"
		}
	}
	return CookieDTO{
		ID:             c.ID,
		Email:          c.Email,
		IsActive:       c.IsActive == 1,
		LastBalance:    c.LastBalance,
		LastError:      c.LastError,
		LastUsedAt:     c.LastUsedAt,
		LastCheckedAt:  c.LastCheckedAt,
		DisabledReason: c.DisabledReason,
		DisabledAt:     c.DisabledAt,
		CreatedAt:      c.CreatedAt,
		Status:         status,
	}
}

// ListCookies returns every cookie row, newest first.
func (a *App) ListCookies() ([]CookieDTO, error) {
	rows, err := a.store.ListCookies()
	if err != nil {
		return nil, err
	}
	out := make([]CookieDTO, 0, len(rows))
	for _, c := range rows {
		out = append(out, cookieToDTO(c))
	}
	return out, nil
}

// AddCookieResult is what the frontend gets after a validated add.
type AddCookieResult struct {
	Email   string `json:"email"`
	Balance int64  `json:"balance"`
}

// AddCookie validates the raw auth payload (cookie string + optional token=)
// against Leonardo, persists it on success, and returns email + balance.
func (a *App) AddCookie(rawAuthValue string) (*AddCookieResult, error) {
	info, err := a.service.AddCookieValidated(rawAuthValue)
	if err != nil {
		return nil, err
	}
	return &AddCookieResult{Email: info.Email, Balance: info.Balance}, nil
}

// UpdateCookie replaces an existing cookie's payload with a freshly pasted
// one, validating against Leonardo first. Returns the new email/balance.
func (a *App) UpdateCookie(id int64, rawAuthValue string) (*AddCookieResult, error) {
	info, err := a.service.UpdateCookieValidated(id, rawAuthValue)
	if err != nil {
		return nil, err
	}
	a.emitCookiesChanged()
	return &AddCookieResult{Email: info.Email, Balance: info.Balance}, nil
}

// DeleteCookie removes a cookie row by id.
func (a *App) DeleteCookie(id int64) error {
	return a.store.DeleteCookie(id)
}

// ToggleCookie enables or disables a cookie without deleting it.
func (a *App) ToggleCookie(id int64, enabled bool) error {
	return a.store.ToggleCookie(id, enabled)
}

// CookieRefreshResult summarises a bulk profile/session refresh run.
type CookieRefreshResult struct {
	Checked int `json:"checked"`
	OK      int `json:"ok"`
}

// RefreshCookieProfiles re-fetches balance + email for every cookie. Disabled
// cookies are not auto-disabled further; depleted ones get marked DEPLETED.
func (a *App) RefreshCookieProfiles() (*CookieRefreshResult, error) {
	res, err := a.service.RefreshCookieProfiles()
	if err != nil {
		return nil, err
	}
	return &CookieRefreshResult{Checked: res.Checked, OK: res.OK}, nil
}

// RefreshCookieSessions re-resolves the JWT for every cookie via TLS impersonation.
func (a *App) RefreshCookieSessions() (*CookieRefreshResult, error) {
	res, err := a.service.RefreshCookieSessions()
	if err != nil {
		return nil, err
	}
	return &CookieRefreshResult{Checked: res.Checked, OK: res.OK}, nil
}

// CookieHealth aggregates status counts for the dashboard hero cards.
type CookieHealth struct {
	Total          int   `json:"total"`
	Ready          int   `json:"ready"`
	Depleted       int   `json:"depleted"`
	Disabled       int   `json:"disabled"`
	TotalBalance   int64 `json:"total_balance"`
	ActiveBalance  int64 `json:"active_balance"`
}

// CookieHealth returns aggregated counts for the dashboard hero cards.
func (a *App) CookieHealth() (*CookieHealth, error) {
	rows, err := a.store.ListCookies()
	if err != nil {
		return nil, err
	}
	out := CookieHealth{Total: len(rows)}
	for _, c := range rows {
		out.TotalBalance += c.LastBalance
		switch {
		case c.IsActive != 1:
			out.Disabled++
		case c.LastBalance > 0:
			out.Ready++
			out.ActiveBalance += c.LastBalance
		default:
			out.Depleted++
		}
	}
	return &out, nil
}

// ----- Settings ------------------------------------------------------------

// GetSetting returns a stored value or the provided fallback.
func (a *App) GetSetting(key, fallback string) (string, error) {
	return a.store.GetSetting(key, fallback)
}

// SetSetting writes a value (creates the key when missing).
func (a *App) SetSetting(key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("setting key is required")
	}
	return a.store.SetSetting(key, value)
}

// ----- Image generation ----------------------------------------------------

// ImageGenerateRequest is the JSON-friendly request from the UI.
type ImageGenerateRequest struct {
	Prompt              string   `json:"prompt"`
	ModelID             string   `json:"modelId"`
	N                   int      `json:"n"`
	AspectRatio         string   `json:"aspectRatio"`
	ReferenceImageURLs  []string `json:"referenceImageURLs"`
	ReferenceImageIDs   []string `json:"referenceImageIds"` // pre-uploaded ids
}

// GenerateImage delegates to LeonardoPool.Generate. The frontend gets back
// the raw provider response so it can render image URLs and metadata.
func (a *App) GenerateImage(req ImageGenerateRequest) (*service.GenerateResponse, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	modelID := strings.TrimSpace(req.ModelID)
	if modelID == "" {
		def, err := a.store.DefaultModelID()
		if err != nil {
			return nil, err
		}
		modelID = def
	}
	if modelID == "" {
		return nil, fmt.Errorf("no model selected and no default configured")
	}
	aspect := strings.TrimSpace(req.AspectRatio)
	if aspect == "" {
		aspect, _ = a.store.GetSetting("default_aspect_ratio", "1:1")
	}
	n := req.N
	if n <= 0 {
		n = 1
	}
	log.Printf("[generate.image] model=%s aspect=%s n=%d urls=%d ids=%d",
		modelID, aspect, n, len(req.ReferenceImageURLs), len(req.ReferenceImageIDs))
	res, err := a.service.Generate(service.GenerateRequest{
		Prompt:             req.Prompt,
		N:                  n,
		ModelID:            modelID,
		AspectRatio:        aspect,
		ReferenceImageURLs: req.ReferenceImageURLs,
		ReferenceImageIDs:  req.ReferenceImageIDs,
	})
	if err != nil {
		log.Printf("[generate.image] error: %v", err)
		return nil, err
	}
	log.Printf("[generate.image] success: gen=%s urls=%d", res.Provider.GenerationID, len(res.Data))
	a.emitCookiesChanged()
	return res, nil
}

// ----- Video generation ----------------------------------------------------

// VideoGenerateRequest mirrors service.VideoRequest with JSON-friendly tags.
type VideoGenerateRequest struct {
	Prompt      string `json:"prompt"`
	ModelSlug   string `json:"modelSlug"`
	AspectRatio string `json:"aspectRatio"`
	Resolution  string `json:"resolution"`
	Duration    int    `json:"duration"`
	Audio       bool   `json:"audio"`
	ImageURL    string `json:"imageURL"`
	ImageID     string `json:"imageId"` // pre-uploaded init image id
}

// GenerateVideo runs Seedance/video pipeline through the cookie pool.
func (a *App) GenerateVideo(req VideoGenerateRequest) (*service.VideoResponse, error) {
	if strings.TrimSpace(req.Prompt) == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	res, err := a.service.GenerateVideo(service.VideoRequest{
		Prompt:      req.Prompt,
		ModelSlug:   req.ModelSlug,
		AspectRatio: req.AspectRatio,
		Resolution:  req.Resolution,
		Duration:    req.Duration,
		Audio:       req.Audio,
		ImageURL:    req.ImageURL,
		ImageID:     req.ImageID,
	})
	if err != nil {
		return nil, err
	}
	a.emitCookiesChanged()
	return res, nil
}

// emitCookiesChanged signals the frontend that cookie balance/state changed
// so it can refetch lists without polling. Safe no-op when ctx is nil
// (e.g. pre-startup or in tests).
func (a *App) emitCookiesChanged() {
	if a.ctx == nil {
		return
	}
	wailsruntime.EventsEmit(a.ctx, "cookies:changed")
}

// ----- Filesystem dialogs --------------------------------------------------

// OpenDirectoryDialog shows the OS-native folder picker. Returns "" on cancel.
func (a *App) OpenDirectoryDialog(currentPath string) (string, error) {
	if a.ctx == nil {
		return "", fmt.Errorf("desktop: app not ready")
	}
	opts := wailsruntime.OpenDialogOptions{
		Title: "Choose folder",
	}
	if currentPath != "" {
		opts.DefaultDirectory = currentPath
	}
	return wailsruntime.OpenDirectoryDialog(a.ctx, opts)
}

// OpenInFileManager opens the given path in the OS file manager.
// Uses xdg-open / open / explorer depending on platform via Wails browser pkg.
func (a *App) OpenInFileManager(path string) error {
	abs := strings.TrimSpace(path)
	if abs == "" {
		return fmt.Errorf("path is required")
	}
	// Resolve relative paths against the data dir so settings like
	// "data/generated" still locate the right folder.
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(defaultDataDir(), abs)
	}
	if _, err := os.Stat(abs); err != nil {
		// Auto-create the directory before opening so brand-new save targets
		// don't 404 the file manager call.
		if os.IsNotExist(err) {
			if mkErr := os.MkdirAll(abs, 0o755); mkErr != nil {
				return mkErr
			}
		} else {
			return err
		}
	}
	wailsruntime.BrowserOpenURL(a.ctx, "file://"+abs)
	return nil
}

// DownloadAsset downloads a remote URL to a user-chosen location via the
// native save dialog. Returns the absolute path written, or "" if the user
// cancelled. Used by the Lightbox preview download button.
func (a *App) DownloadAsset(url string, suggestedName string) (string, error) {
	if a.ctx == nil {
		return "", fmt.Errorf("desktop: app not ready")
	}
	url = strings.TrimSpace(url)
	if url == "" {
		return "", fmt.Errorf("url is required")
	}

	suggested := strings.TrimSpace(suggestedName)
	if suggested == "" {
		suggested = filepath.Base(stripQueryString(url))
	}
	if suggested == "" || suggested == "/" {
		suggested = "leonardo-asset"
	}

	// Default to ~/Downloads when available, fall back to user config dir.
	defaultDir := ""
	if home, err := os.UserHomeDir(); err == nil {
		downloads := filepath.Join(home, "Downloads")
		if _, statErr := os.Stat(downloads); statErr == nil {
			defaultDir = downloads
		} else {
			defaultDir = home
		}
	}

	target, err := wailsruntime.SaveFileDialog(a.ctx, wailsruntime.SaveDialogOptions{
		Title:                "Save asset",
		DefaultDirectory:     defaultDir,
		DefaultFilename:      suggested,
		CanCreateDirectories: true,
	})
	if err != nil {
		return "", err
	}
	if target == "" {
		// User cancelled — return empty string, no error.
		return "", nil
	}

	body, _, err := a.service.Client().Download(url)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(target, body, 0o644); err != nil {
		return "", err
	}
	return target, nil
}

// stripQueryString removes any ?query suffix so filename derivation works
// against URLs that include cache busters.
func stripQueryString(rawURL string) string {
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		return rawURL[:i]
	}
	return rawURL
}

// UploadLocalImage forwards a raw image (drag-drop / file picker) to
// Leonardo via the cookie pool and returns the init image id.
//
// We accept two positional string args because Wails marshals positional
// arguments more reliably than struct payloads across the JS↔Go bridge.
func (a *App) UploadLocalImage(base64Payload, extension string) (string, error) {
	raw := strings.TrimSpace(base64Payload)
	if raw == "" {
		return "", fmt.Errorf("empty image payload")
	}
	// Strip data URL prefix (e.g. "data:image/png;base64,") if present so
	// the frontend can pass either form.
	if i := strings.Index(raw, ","); i >= 0 && strings.HasPrefix(raw, "data:") {
		raw = raw[i+1:]
	}
	bytes, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("invalid base64: %w", err)
	}
	ext := strings.ToLower(strings.TrimSpace(extension))
	ext = strings.TrimPrefix(ext, ".")
	if ext == "" {
		ext = "jpg"
	}
	log.Printf("[upload] received: bytes=%d ext=%s", len(bytes), ext)
	id, err := a.service.UploadLocalImage(bytes, ext)
	if err != nil {
		log.Printf("[upload] failed: %v", err)
		return "", err
	}
	log.Printf("[upload] success: id=%s", id)
	return id, nil
}

// ----- Models --------------------------------------------------------------

// ModelDTO is the JSON-friendly row from the models table.
type ModelDTO struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ModelID   string `json:"modelId"`
	SDVersion string `json:"sdVersion"`
	IsDefault bool   `json:"isDefault"`
	CreatedAt int64  `json:"createdAt"`
}

// SyncImageModels pulls the official Leonardo catalog and upserts it locally.
// Used by the Models page "Sync" button.
func (a *App) SyncImageModels() (*service.ModelSyncResult, error) {
	res, err := a.service.SyncImageModels()
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// ListImageModels returns all image models from the local DB.
func (a *App) ListImageModels() ([]ModelDTO, error) {
	rows, err := a.store.ListModels()
	if err != nil {
		return nil, err
	}
	out := make([]ModelDTO, 0, len(rows))
	for _, m := range rows {
		out = append(out, ModelDTO{
			ID:        m.ID,
			Name:      m.Name,
			ModelID:   m.ModelID,
			SDVersion: m.SDVersion,
			IsDefault: m.IsDefault == 1,
			CreatedAt: m.CreatedAt,
		})
	}
	return out, nil
}

// AddImageModel inserts a new image model entry.
func (a *App) AddImageModel(name, modelID string) error {
	return a.store.AddModel(name, modelID)
}

// DeleteImageModel removes a model row by id.
func (a *App) DeleteImageModel(id int64) error {
	return a.store.DeleteModel(id)
}

// SetDefaultImageModel promotes a row to default.
func (a *App) SetDefaultImageModel(id int64) error {
	return a.store.SetDefaultModel(id)
}

// VideoModelDTO is the catalog entry shape exposed to the UI.
type VideoModelDTO struct {
	Slug             string   `json:"slug"`
	DefaultMode      string   `json:"defaultMode"`
	SupportedModes   []string `json:"supportedModes"`
	DurationOptions  []int    `json:"durationOptions"`
	DefaultDuration  int      `json:"defaultDuration"`
	SupportsAudio    bool     `json:"supportsAudio"`
	SupportsRefImage bool     `json:"supportsRefImage"`
	DefaultAspect    string   `json:"defaultAspect"`
}

// ListVideoModels returns the static video model catalog.
func (a *App) ListVideoModels() []VideoModelDTO {
	out := make([]VideoModelDTO, 0, len(service.VideoModels))
	for _, vm := range service.VideoModels {
		out = append(out, VideoModelDTO{
			Slug:             vm.Slug,
			DefaultMode:      vm.DefaultMode,
			SupportedModes:   append([]string(nil), vm.SupportedModes...),
			DurationOptions:  append([]int(nil), vm.DurationOptions...),
			DefaultDuration:  vm.DefaultDuration,
			SupportsAudio:    vm.SupportsAudio,
			SupportsRefImage: vm.SupportsRefImage,
			DefaultAspect:    vm.DefaultAspect,
		})
	}
	return out
}

// ----- Library / generation logs ------------------------------------------

// GenerationLogDTO is a JSON-friendly row from generation_logs.
type GenerationLogDTO struct {
	ID                   int64    `json:"id"`
	ProviderGenerationID string   `json:"providerGenerationID"`
	UsedCookieID         int64    `json:"usedCookieID"`
	ModelID              string   `json:"modelID"`
	AspectRatio          string   `json:"aspectRatio"`
	Prompt               string   `json:"prompt"`
	ImageURLs            []string `json:"imageURLs"`
	SavedFiles           []string `json:"savedFiles"`
	SaveEnabled          bool     `json:"saveEnabled"`
	Status               string   `json:"status"`
	ErrorMessage         string   `json:"errorMessage"`
	CreatedAt            int64    `json:"createdAt"`
}

// ListGenerationLogs returns the most recent generations (capped at 200).
func (a *App) ListGenerationLogs(limit int) ([]GenerationLogDTO, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := a.store.ListGenerationLogs(limit)
	if err != nil {
		return nil, err
	}
	out := make([]GenerationLogDTO, 0, len(rows))
	for _, r := range rows {
		urls := []string{}
		_ = json.Unmarshal([]byte(r.ImageURLsJSON), &urls)
		files := []string{}
		_ = json.Unmarshal([]byte(r.SavedFilesJSON), &files)
		out = append(out, GenerationLogDTO{
			ID:                   r.ID,
			ProviderGenerationID: r.ProviderGenerationID,
			UsedCookieID:         r.UsedCookieID,
			ModelID:              r.ModelID,
			AspectRatio:          r.AspectRatio,
			Prompt:               r.Prompt,
			ImageURLs:            urls,
			SavedFiles:           files,
			SaveEnabled:          r.SaveEnabled == 1,
			Status:               r.Status,
			ErrorMessage:         r.ErrorMessage,
			CreatedAt:            r.CreatedAt,
		})
	}
	return out, nil
}

// AspectRatioOption is one supported aspect ratio entry for the image UI.
type AspectRatioOption struct {
	Label  string `json:"label"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// ListImageAspects returns supported aspect ratios for image generation.
func (a *App) ListImageAspects() []AspectRatioOption {
	out := make([]AspectRatioOption, 0, len(service.AspectSize))
	// Stable order for the UI.
	order := []string{"1:1", "16:9", "9:16", "4:3"}
	for _, key := range order {
		size, ok := service.AspectSize[key]
		if !ok {
			continue
		}
		out = append(out, AspectRatioOption{Label: key, Width: size[0], Height: size[1]})
	}
	return out
}

// ----- Internal helpers ----------------------------------------------------

// defaultDataDir returns ~/.config/leostudio (Linux/macOS) or
// %APPDATA%/leostudio (Windows). Falls back to ./data when the OS dir is
// unavailable so the app still works in restricted environments.
func defaultDataDir() string {
	if env := os.Getenv("LEOSTUDIO_DATA_DIR"); env != "" {
		return env
	}
	cfg, err := os.UserConfigDir()
	if err != nil || cfg == "" {
		return "data"
	}
	return filepath.Join(cfg, "leostudio")
}
