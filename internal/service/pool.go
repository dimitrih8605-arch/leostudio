// Package service contains the Leonardo cookie pool orchestrator. It mirrors
// app/leonardo_service.py: rotate cookies, refresh fallback tokens, auto-
// disable on auth/balance failure, and run a generation end-to-end.
package service

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hirotomasato/leostudio/internal/leonardo"
	"github.com/hirotomasato/leostudio/internal/store"
)

// LeonardoPool wires the cookie store with the Leonardo client.
type LeonardoPool struct {
	store *store.Store
	api   *leonardo.Client
}

// NewLeonardoPool constructs the orchestrator.
func NewLeonardoPool(st *store.Store, client *leonardo.Client) *LeonardoPool {
	return &LeonardoPool{store: st, api: client}
}

// Client exposes the underlying Leonardo HTTP client. Used by the desktop
// layer to download assets through the same TLS-impersonating client we
// already use for image upload + GraphQL.
func (p *LeonardoPool) Client() *leonardo.Client {
	return p.api
}

// authFailCooldown matches AUTH_FAIL_COOLDOWN_SECONDS in Python.
const authFailCooldown = 300 * time.Second

// PublicError carries a status code + safe message for HTTP handlers.
type PublicError struct {
	Status  int
	Message string
}

func (e *PublicError) Error() string { return e.Message }

func newPublicError(status int, msg string) *PublicError {
	return &PublicError{Status: status, Message: msg}
}

// GenerateRequest collects parameters for a single generation attempt.
type GenerateRequest struct {
	Prompt              string
	N                   int
	ModelID             string
	AspectRatio         string
	ReferenceImageURLs  []string
	ReferenceImageIDs   []string // pre-uploaded init image ids; merged with ReferenceImageURLs results
	SaveResults         *bool    // nil = follow auto_save_images setting
}

// GenerateResponse mirrors the OpenAI-compatible response shape.
type GenerateResponse struct {
	Created  int64               `json:"created"`
	Data     []GenerateDataItem  `json:"data"`
	Provider GenerateProviderMeta `json:"provider"`
}

// GenerateDataItem is one generated image URL entry.
type GenerateDataItem struct {
	URL string `json:"url"`
}

// GenerateProviderMeta describes which cookie/model handled the job.
type GenerateProviderMeta struct {
	GenerationID    string   `json:"generation_id"`
	UsedCookieID    int64    `json:"used_cookie_id"`
	AspectRatio     string   `json:"aspect_ratio"`
	ModelID         string   `json:"model_id"`
	SavedFiles      []string `json:"saved_files"`
	AutoSaveEnabled bool     `json:"auto_save_enabled"`
	SaveError       string   `json:"save_error,omitempty"`
}

// Generate runs the full pipeline: rotate cookies, upload references,
// create generation, poll, optionally save images, log result.
func (p *LeonardoPool) Generate(req GenerateRequest) (*GenerateResponse, error) {
	width, height := ResolveSize(req.AspectRatio)
	quantity := req.N
	if quantity < 1 {
		quantity = 1
	}
	if quantity > 4 {
		quantity = 4
	}

	cookies, err := p.store.ListActiveCookies()
	if err != nil {
		return nil, fmt.Errorf("service: list cookies: %w", err)
	}
	if len(cookies) == 0 {
		return nil, newPublicError(400, "No active cookie configured in admin panel")
	}

	var errs []string

	for _, cookie := range cookies {
		if p.shouldSkipCookieNow(cookie) {
			errs = append(errs, fmt.Sprintf("cookie#%d: cooldown (auth recently failed)", cookie.ID))
			continue
		}

		// Each cookie gets up to two attempts: the first may resolve a fresh
		// token from the cookie itself, the second falls back to forcing a
		// resolve again or recognising auth-only failures.
		for attempt := 0; attempt < 2; attempt++ {
			token := p.resolveToken(cookie.Value)
			if token == "" {
				if attempt == 0 {
					continue
				}
				p.disableCookie(cookie.ID, "AUTH_EXPIRED", "Failed to fetch token from cookie")
				errs = append(errs, fmt.Sprintf("cookie#%d: failed token (auto-disabled)", cookie.ID))
				break
			}

			info, err := p.api.GetUserInfo(token)
			if err != nil {
				if isAuthError(err.Error()) && attempt == 0 {
					continue
				}
				if isAuthError(err.Error()) {
					p.disableCookie(cookie.ID, "AUTH_EXPIRED", err.Error())
				}
				errs = append(errs, fmt.Sprintf("cookie#%d: %s", cookie.ID, err.Error()))
				break
			}

			p.refreshFallbackToken(cookie.ID, cookie.Value, token)
			_ = p.store.UpdateCookieProfile(cookie.ID, info.Email, info.Tokens)

			if info.Tokens <= 0 {
				p.disableCookie(cookie.ID, "DEPLETED", "Token balance is empty")
				errs = append(errs, fmt.Sprintf("cookie#%d: depleted (auto-disabled)", cookie.ID))
				break
			}

			// Upload reference images (max 3, matching Python). Pre-uploaded
			// IDs (from desktop drag-drop / file picker) are prepended so they
			// take priority when caller supplies both forms.
			var initImageIDs []string
			for _, id := range req.ReferenceImageIDs {
				if id == "" {
					continue
				}
				initImageIDs = append(initImageIDs, id)
				if len(initImageIDs) >= 3 {
					break
				}
			}

			refs := req.ReferenceImageURLs
			remaining := 3 - len(initImageIDs)
			if remaining < 0 {
				remaining = 0
			}
			if len(refs) > remaining {
				refs = refs[:remaining]
			}
			uploadFailed := false
			for _, refURL := range refs {
				id, err := p.api.UploadImageURL(token, refURL)
				if err != nil {
					if isAuthError(err.Error()) && attempt == 0 {
						uploadFailed = true
						break
					}
					if isAuthError(err.Error()) {
						p.disableCookie(cookie.ID, "AUTH_EXPIRED", err.Error())
					}
					errs = append(errs, fmt.Sprintf("cookie#%d: upload ref: %s", cookie.ID, err.Error()))
					uploadFailed = true
					break
				}
				initImageIDs = append(initImageIDs, id)
			}
			if uploadFailed {
				if attempt == 0 {
					continue
				}
				break
			}

			sdVersion, _ := p.store.GetSDVersion(req.ModelID)

			genID, err := p.api.CreateGeneration(token, leonardo.GenerateInput{
				Prompt:       req.Prompt,
				ModelID:      req.ModelID,
				Width:        width,
				Height:       height,
				Quantity:     quantity,
				InitImageIDs: initImageIDs,
				SDVersion:    sdVersion,
			})
			if err != nil {
				if isAuthError(err.Error()) && attempt == 0 {
					continue
				}
				if isAuthError(err.Error()) {
					p.disableCookie(cookie.ID, "AUTH_EXPIRED", err.Error())
				}
				errs = append(errs, fmt.Sprintf("cookie#%d: %s", cookie.ID, err.Error()))
				break
			}

			result := p.api.WaitForCompletion(token, genID, 300*time.Second, 4*time.Second)
			if !result.Success {
				if isAuthError(result.Error) && attempt == 0 {
					continue
				}
				if isAuthError(result.Error) {
					p.disableCookie(cookie.ID, "AUTH_EXPIRED", result.Error)
				}
				errs = append(errs, fmt.Sprintf("cookie#%d: %s", cookie.ID, result.Error))
				break
			}

			_ = p.store.MarkCookieUsed(cookie.ID)

			autoSaveEnabled := false
			if v, _ := p.store.GetSetting("auto_save_images", "0"); v == "1" {
				autoSaveEnabled = true
			}
			if req.SaveResults != nil {
				autoSaveEnabled = *req.SaveResults
			}

			var savedFiles []string
			var saveErrMsg string
			if autoSaveEnabled && len(result.Images) > 0 {
				files, err := p.saveGeneratedImages(genID, result.Images)
				savedFiles = files
				if err != nil {
					saveErrMsg = err.Error()
				}
			}

			_ = p.store.AddGenerationLog(
				genID,
				cookie.ID,
				req.ModelID,
				req.AspectRatio,
				req.Prompt,
				result.Images,
				savedFiles,
				autoSaveEnabled,
				"success",
				"",
			)

			items := make([]GenerateDataItem, 0, len(result.Images))
			for _, u := range result.Images {
				items = append(items, GenerateDataItem{URL: u})
			}
			return &GenerateResponse{
				Created: time.Now().Unix(),
				Data:    items,
				Provider: GenerateProviderMeta{
					GenerationID:    genID,
					UsedCookieID:    cookie.ID,
					AspectRatio:     req.AspectRatio,
					ModelID:         req.ModelID,
					SavedFiles:      orEmpty(savedFiles),
					AutoSaveEnabled: autoSaveEnabled,
					SaveError:       saveErrMsg,
				},
			}, nil
		}
	}

	detail := "All cookies failed."
	if len(errs) > 0 {
		if len(errs) > 6 {
			errs = errs[:6]
		}
		detail = "All cookies failed. " + strings.Join(errs, " | ")
	}
	return nil, newPublicError(503, detail)
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (p *LeonardoPool) saveGeneratedImages(generationID string, urls []string) ([]string, error) {
	rawDir, _ := p.store.GetSetting("save_images_dir", "data/generated")
	dir := strings.TrimSpace(rawDir)
	if dir == "" {
		dir = "data/generated"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	saved := make([]string, 0, len(urls))
	ts := time.Now().Unix()
	for i, u := range urls {
		body, _, err := p.api.Download(u)
		if err != nil {
			return saved, err
		}
		ext := filepath.Ext(stripQuery(u))
		if ext == "" {
			ext = ".jpg"
		}
		name := fmt.Sprintf("%s_%d_%d%s", generationID, i+1, ts, ext)
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return saved, err
		}
		saved = append(saved, path)
	}
	return saved, nil
}

func stripQuery(rawURL string) string {
	if i := strings.IndexByte(rawURL, '?'); i >= 0 {
		return rawURL[:i]
	}
	return rawURL
}

// resolveToken implements the same priority order as Python _resolve_token:
//
//  1. Resolve from cookie payload via Leonardo client (always preferred).
//  2. Use parsed `token=...` line if it is fresh + likely Leonardo.
//  3. Use raw value as JWT if it qualifies.
func (p *LeonardoPool) resolveToken(rawAuthValue string) string {
	value := strings.TrimSpace(rawAuthValue)
	token, cookie := extractAuthParts(value)

	cookiePayload := strings.TrimSpace(cookie)
	if cookiePayload == "" {
		cookiePayload = value
	}
	if strings.HasPrefix(strings.ToLower(cookiePayload), "cookie:") {
		cookiePayload = strings.TrimSpace(cookiePayload[len("cookie:"):])
	}

	if cookiePayload != "" {
		if t := p.api.GetTokenFromCookie(cookiePayload); t != "" && leonardo.LooksLikeJWT(t) {
			return t
		}
	}

	if leonardo.IsFreshToken(token, 120) && leonardo.IsLikelyLeonardoToken(token) {
		return token
	}
	if leonardo.IsFreshToken(value, 120) && leonardo.IsLikelyLeonardoToken(value) {
		return value
	}
	return ""
}

// refreshFallbackToken updates the stored cookie value so that a fresh token
// fallback is persisted alongside the cookie. Mirrors Python helper.
func (p *LeonardoPool) refreshFallbackToken(cookieID int64, rawAuthValue, resolvedToken string) {
	parsedToken, parsedCookie := extractAuthParts(rawAuthValue)
	cookiePayload := strings.TrimSpace(parsedCookie)
	if cookiePayload == "" {
		raw := strings.TrimSpace(rawAuthValue)
		if strings.Contains(raw, ";") && strings.Contains(raw, "=") {
			cookiePayload = raw
		}
	}
	if cookiePayload == "" {
		return
	}

	tokenForStore := resolvedToken
	if tokenForStore == "" {
		tokenForStore = parsedToken
	}
	current := strings.TrimSpace(rawAuthValue)
	next := composeStoreAuthValue(cookiePayload, tokenForStore)
	if next != "" && next != current {
		_, _ = p.store.UpdateCookieValue(cookieID, next)
	}
}

func composeStoreAuthValue(cookiePayload, token string) string {
	normalized := strings.TrimSpace(cookiePayload)
	if strings.HasPrefix(strings.ToLower(normalized), "cookie:") {
		normalized = strings.TrimSpace(normalized[len("cookie:"):])
	}
	tokenTrim := strings.TrimSpace(token)
	if tokenTrim != "" && leonardo.IsFreshToken(tokenTrim, 300) && leonardo.IsLikelyLeonardoToken(tokenTrim) {
		return fmt.Sprintf("cookie=%s\ntoken=%s", normalized, tokenTrim)
	}
	return normalized
}

// extractAuthParts mirrors Python's _extract_auth_parts: parses lines like
// `cookie=...`, `token=...`, raw cookie strings, or bare JWTs.
func extractAuthParts(rawAuthValue string) (token, cookie string) {
	raw := strings.TrimSpace(rawAuthValue)
	if raw == "" {
		return "", ""
	}

	var lines []string
	for _, l := range strings.Split(raw, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		lines = append(lines, l)
	}
	if len(lines) == 0 {
		lines = []string{raw}
	}

	for _, line := range lines {
		if strings.HasPrefix(strings.ToLower(line), "cookie:") {
			line = strings.TrimSpace(line[len("cookie:"):])
		}

		if strings.Contains(line, "=") {
			key, value, _ := strings.Cut(line, "=")
			key = strings.ToLower(strings.TrimSpace(key))
			value = strings.TrimSpace(value)

			switch {
			case key == "token" && value != "":
				token = value
				continue
			case key == "cookie" && value != "":
				cookie = value
				continue
			}

			if strings.Contains(line, ";") {
				cookie = line
				continue
			}

			if strings.Contains(key, "next-auth") ||
				strings.HasPrefix(key, "__host-next-auth") ||
				strings.HasPrefix(key, "__secure-next-auth") ||
				strings.Contains(key, "better-auth") ||
				strings.HasPrefix(key, "__secure-better-auth") ||
				strings.Contains(key, "authjs") ||
				strings.HasPrefix(key, "__secure-authjs") ||
				strings.HasPrefix(key, "__host-authjs") {
				cookie = line
				continue
			}
		} else {
			if strings.Count(line, ".") == 2 && !strings.Contains(line, " ") && len(line) > 40 {
				token = line
				continue
			}
			if strings.Contains(line, ";") && strings.Contains(line, "=") {
				cookie = line
			}
		}
	}
	return token, cookie
}

// shouldSkipCookieNow is the cooldown logic: skip cookies whose last error
// was an auth failure within the cooldown window.
func (p *LeonardoPool) shouldSkipCookieNow(c store.Cookie) bool {
	if !isAuthError(c.LastError) {
		return false
	}
	if c.LastCheckedAt == 0 {
		return false
	}
	return time.Since(time.Unix(c.LastCheckedAt, 0)) < authFailCooldown
}

func (p *LeonardoPool) disableCookie(id int64, reason, message string) {
	_ = p.store.MarkCookieError(id, message)
	_ = p.store.AutoDisableCookie(id, reason)
}

func isAuthError(message string) bool {
	text := strings.TrimSpace(strings.ToLower(message))
	if text == "" {
		return false
	}
	markers := []string{
		"jwt expired",
		"token expired",
		"invalid token",
		"invalid bearer",
		"unauthorized",
		"forbidden",
		"access denied",
		"401",
		"403",
		"session refresh gagal",
		"failed token",
		"failed to fetch token",
		"auth tidak valid",
		"authentication",
	}
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// AddCookieValidated mirrors Python add_cookie_validated: only persist a
// cookie that resolves to a usable JWT and report the balance back.
func (p *LeonardoPool) AddCookieValidated(rawAuthValue string) (UserInfoResult, error) {
	storeValue, info, err := p.validateAndPrepareCookie(rawAuthValue)
	if err != nil {
		return UserInfoResult{}, err
	}
	if err := p.store.AddCookie(storeValue); err != nil {
		return UserInfoResult{}, err
	}
	saved, err := p.store.GetCookieByValue(storeValue)
	if err != nil {
		return UserInfoResult{}, err
	}
	if saved != nil {
		_ = p.store.UpdateCookieProfile(saved.ID, info.Email, info.Tokens)
		if info.Tokens > 0 {
			_ = p.store.MarkCookieUsed(saved.ID)
		}
	}
	return UserInfoResult{Email: info.Email, Balance: info.Tokens}, nil
}

// UpdateCookieValidated replaces an existing cookie's payload after running
// the same validation as AddCookieValidated. Used when the user pastes a
// fresh cookie to an existing slot (e.g. after logging in again).
func (p *LeonardoPool) UpdateCookieValidated(id int64, rawAuthValue string) (UserInfoResult, error) {
	storeValue, info, err := p.validateAndPrepareCookie(rawAuthValue)
	if err != nil {
		return UserInfoResult{}, err
	}
	changed, err := p.store.UpdateCookieValue(id, storeValue)
	if err != nil {
		return UserInfoResult{}, err
	}
	if !changed {
		return UserInfoResult{}, errors.New("Cookie sudah pernah disimpan persis sama (no change).")
	}
	_ = p.store.UpdateCookieProfile(id, info.Email, info.Tokens)
	// Re-enable when the operator pasted a fresh cookie into a disabled slot.
	if info.Tokens > 0 {
		_ = p.store.ToggleCookie(id, true)
		_ = p.store.MarkCookieUsed(id)
	}
	return UserInfoResult{Email: info.Email, Balance: info.Tokens}, nil
}

// validateAndPrepareCookie centralises the cookie validation pipeline used
// by both Add and Update flows. It returns the canonical store value plus
// the resolved user info.
func (p *LeonardoPool) validateAndPrepareCookie(rawAuthValue string) (string, leonardo.UserInfo, error) {
	value := strings.TrimSpace(rawAuthValue)
	if value == "" {
		return "", leonardo.UserInfo{}, errors.New("Cookie/token tidak boleh kosong")
	}

	parsedToken, parsedCookie := extractAuthParts(value)
	cookiePayload := strings.TrimSpace(parsedCookie)
	if strings.HasPrefix(strings.ToLower(cookiePayload), "cookie:") {
		cookiePayload = strings.TrimSpace(cookiePayload[len("cookie:"):])
	}

	if cookiePayload == "" || !strings.Contains(cookiePayload, ";") || !strings.Contains(cookiePayload, "=") {
		if parsedToken != "" || leonardo.LooksLikeJWT(value) {
			return "", leonardo.UserInfo{}, errors.New("Input JWT ditolak. Wajib paste full cookie string dari browser.")
		}
		return "", leonardo.UserInfo{}, errors.New("Format cookie tidak valid. Wajib full cookie string (name=value; ...)")
	}

	lower := strings.ToLower(cookiePayload)
	hasMarker := strings.Contains(lower, "next-auth.session-token") ||
		strings.Contains(lower, "authjs.session-token") ||
		strings.Contains(lower, "__secure-next-auth.session-token") ||
		strings.Contains(lower, "__secure-authjs.session-token") ||
		strings.Contains(lower, "__host-next-auth.csrf-token") ||
		strings.Contains(lower, "next-auth.csrf-token") ||
		strings.Contains(lower, "better-auth.session_token") ||
		strings.Contains(lower, "better-auth.session-token") ||
		strings.Contains(lower, "better-auth.session_data")
	if !hasMarker {
		return "", leonardo.UserInfo{}, errors.New("Cookie bukan session Leonardo yang valid. Ambil ulang dari extension saat sudah login app.leonardo.ai")
	}

	token := p.api.GetTokenFromCookie(cookiePayload)
	if token == "" && parsedToken != "" {
		candidate := strings.TrimSpace(parsedToken)
		if leonardo.IsFreshToken(candidate, 300) && leonardo.IsLikelyLeonardoToken(candidate) {
			token = candidate
		}
	}
	if token == "" {
		return "", leonardo.UserInfo{}, errors.New("Auth tidak valid: gagal mendapatkan token")
	}
	if strings.Count(token, ".") != 2 {
		return "", leonardo.UserInfo{}, errors.New("Token session tidak valid untuk API (bukan JWT bearer)")
	}

	info, err := p.api.GetUserInfo(token)
	if err != nil {
		return "", leonardo.UserInfo{}, err
	}

	storeValue := composeStoreAuthValue(cookiePayload, token)
	if storeValue == "" {
		storeValue = composeStoreAuthValue(cookiePayload, parsedToken)
	}
	return storeValue, info, nil
}

// UserInfoResult is the public payload returned by AddCookieValidated.
type UserInfoResult struct {
	Email   string
	Balance int64
}

// helpers retained to satisfy package imports
var _ = json.RawMessage{}
var _ = base64.StdEncoding
var _ = url.QueryEscape
