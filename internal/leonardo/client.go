// Package leonardo wraps the Leonardo AI HTTP/GraphQL surface used by the
// service layer. It mirrors app/leonardo_client.py:
//
//   - TLS impersonation against app.leonardo.ai (Vercel checkpoint).
//   - Multi-strategy JWT resolver (better-auth, next-auth, raw cookie).
//   - GraphQL helpers for user info, image upload, generation.
package leonardo

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/imroc/req/v3"
)

const (
	graphqlURL = "https://api.leonardo.ai/v1/graphql"
	authURL    = "https://app.leonardo.ai/api/auth"
	sentryRel  = "6a0bd1b5b7ef23a4f22608a2ed90c5e753cbc669"

	defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/145.0.0.0 Safari/537.36"
)

// Client is the high-level Leonardo API wrapper.
type Client struct {
	gqlClient        *req.Client // standard JSON client for api.leonardo.ai
	impersonatorClient *req.Client // chrome-impersonating client for app.leonardo.ai
	httpClient       *req.Client // plain client for arbitrary downloads
}

// New constructs a client with sensible HTTP timeouts and TLS impersonation.
func New() *Client {
	std := req.C().
		SetTimeout(60 * time.Second).
		SetCommonHeader("user-agent", defaultUserAgent)

	// app.leonardo.ai is behind Vercel Security Checkpoint that fingerprints
	// TLS. ImpersonateChrome makes req use Chrome's TLS profile so the
	// challenge does not trigger.
	imp := req.C().
		SetTimeout(60 * time.Second).
		ImpersonateChrome().
		SetCommonHeader("user-agent", defaultUserAgent)

	plain := req.C().SetTimeout(180 * time.Second)

	return &Client{
		gqlClient:          std,
		impersonatorClient: imp,
		httpClient:         plain,
	}
}

// Download fetches an arbitrary URL, used for reference images and result downloads.
func (c *Client) Download(rawURL string) ([]byte, string, error) {
	resp, err := c.httpClient.R().Get(rawURL)
	if err != nil {
		return nil, "", fmt.Errorf("leonardo: download %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if !resp.IsSuccessState() {
		return nil, "", fmt.Errorf("leonardo: download %s: HTTP %d", rawURL, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	contentType := resp.GetHeader("content-type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return body, contentType, nil
}

// baseHeaders returns the common header set sent on api.leonardo.ai.
func (c *Client) baseHeaders() map[string]string {
	return map[string]string{
		"accept":               "*/*",
		"accept-language":      "en-US,en;q=0.9",
		"content-type":         "application/json",
		"origin":               "https://app.leonardo.ai",
		"referer":              "https://app.leonardo.ai/",
		"x-leo-schema-version": "latest",
		"sec-fetch-dest":       "empty",
		"sec-fetch-mode":       "cors",
		"sec-fetch-site":       "same-site",
	}
}

func makeID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000"
	}
	return fmt.Sprintf("%x", b[:])
}

func (c *Client) sentryHeaders(token string) map[string]string {
	tid := makeID() + makeID()
	headers := c.baseHeaders()
	headers["authorization"] = "Bearer " + token
	headers["sentry-trace"] = fmt.Sprintf("%s-%s-0", tid, makeID())
	headers["baggage"] = strings.Join([]string{
		"sentry-environment=vercel-production",
		"sentry-release=" + sentryRel,
		"sentry-public_key=a851bd902378477eae99cf74c62e142a",
		"sentry-trace_id=" + tid,
		"sentry-org_id=4504767521292288",
		"sentry-sampled=false",
	}, ",")
	return headers
}

// gqlPayload represents a GraphQL operation envelope.
type gqlPayload struct {
	OperationName string         `json:"operationName"`
	Variables     map[string]any `json:"variables"`
	Query         string         `json:"query"`
}

// gql posts a payload to the Leonardo GraphQL endpoint and returns the
// decoded JSON response (data + errors retained).
func (c *Client) gql(token string, payload gqlPayload) (map[string]any, error) {
	resp, err := c.gqlClient.R().
		SetHeaders(c.sentryHeaders(token)).
		SetBody(payload).
		Post(graphqlURL)
	if err != nil {
		return nil, fmt.Errorf("leonardo: gql request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 204 {
		return map[string]any{}, nil
	}
	body, _ := io.ReadAll(resp.Body)
	if !resp.IsSuccessState() {
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return nil, fmt.Errorf("leonardo: gql HTTP %d: %s", resp.StatusCode, snippet)
	}

	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("leonardo: decode gql: %w", err)
	}
	return out, nil
}

// GraphQLErrorMessage extracts a joined error message from a raw response.
// Exported for callers in fetch_models style utilities.
func GraphQLErrorMessage(resp map[string]any) string {
	errs, _ := resp["errors"].([]any)
	if len(errs) == 0 {
		return ""
	}
	var msgs []string
	for _, e := range errs {
		if m, ok := e.(map[string]any); ok {
			if s, ok := m["message"].(string); ok {
				s = strings.TrimSpace(s)
				if s != "" {
					msgs = append(msgs, s)
				}
			}
			continue
		}
		if e != nil {
			msgs = append(msgs, fmt.Sprintf("%v", e))
		}
	}
	return strings.Join(msgs, " | ")
}

// extractCookieValue mimics Python's _extract_cookie_value, including
// reassembly of chunked cookies (`name.0`, `name.1`, ...).
func extractCookieValue(cookieStr, baseName string) string {
	cookies := map[string]string{}
	for _, part := range strings.Split(cookieStr, ";") {
		part = strings.TrimSpace(part)
		if part == "" || !strings.Contains(part, "=") {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		cookies[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}

	if v, ok := cookies[baseName]; ok {
		return v
	}

	type chunk struct {
		idx int
		val string
	}
	var chunks []chunk
	prefix := baseName + "."
	for k, v := range cookies {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		suffix := k[len(prefix):]
		idx := 0
		if _, err := fmt.Sscanf(suffix, "%d", &idx); err == nil && fmt.Sprintf("%d", idx) == suffix {
			chunks = append(chunks, chunk{idx: idx, val: v})
		}
	}
	if len(chunks) == 0 {
		return ""
	}
	// sort manually (small slices).
	for i := 1; i < len(chunks); i++ {
		for j := i; j > 0 && chunks[j-1].idx > chunks[j].idx; j-- {
			chunks[j-1], chunks[j] = chunks[j], chunks[j-1]
		}
	}
	var out strings.Builder
	for _, ch := range chunks {
		out.WriteString(ch.val)
	}
	return out.String()
}

// LooksLikeJWT is true when the value has three base64-url segments.
func LooksLikeJWT(value string) bool {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			isAlnum := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
			if !isAlnum && r != '_' && r != '-' {
				return false
			}
		}
	}
	return true
}

// normalizeTokenCandidate URL-decodes the value and strips a Bearer prefix.
func normalizeTokenCandidate(value any) string {
	s, ok := value.(string)
	if !ok {
		return ""
	}
	decoded, err := url.QueryUnescape(strings.TrimSpace(s))
	if err != nil {
		decoded = strings.TrimSpace(s)
	}
	if strings.HasPrefix(strings.ToLower(decoded), "bearer ") {
		decoded = strings.TrimSpace(decoded[7:])
	}
	return decoded
}

// DecodeJWTPayload returns the decoded claim map for a JWT, or empty.
func DecodeJWTPayload(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	payload := parts[1]
	// Add base64 padding.
	switch len(payload) % 4 {
	case 2:
		payload += "=="
	case 3:
		payload += "="
	}
	raw, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		raw, err = base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil
		}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// IsLikelyLeonardoToken matches Python heuristics for filtering Cognito tokens.
func IsLikelyLeonardoToken(token string) bool {
	if !LooksLikeJWT(token) {
		return false
	}
	payload := DecodeJWTPayload(token)
	if payload == nil {
		return false
	}
	iss, _ := payload["iss"].(string)
	if strings.Contains(strings.ToLower(iss), "cognito-idp") {
		return true
	}
	tokenUse, _ := payload["token_use"].(string)
	tokenUse = strings.ToLower(tokenUse)
	if tokenUse == "id" || tokenUse == "access" {
		return true
	}
	if _, ok := payload["cognito:username"]; ok {
		return true
	}
	if aud, ok := payload["aud"].(string); ok && strings.HasPrefix(aud, "https://cognito-idp") {
		return true
	}
	return false
}

// TokenExp returns the exp claim as a unix timestamp, or 0.
func TokenExp(token string) int64 {
	payload := DecodeJWTPayload(token)
	if payload == nil {
		return 0
	}
	switch v := payload["exp"].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

// IsFreshToken reports whether the JWT exp is at least minTTL seconds away.
// Tokens without exp are treated as fresh (matches Python).
func IsFreshToken(token string, minTTL int64) bool {
	if !LooksLikeJWT(token) {
		return false
	}
	exp := TokenExp(token)
	if exp == 0 {
		return true
	}
	if minTTL < 30 {
		minTTL = 30
	}
	return exp > time.Now().Unix()+minTTL
}

// pickBestToken implements the same ranking as Python (_pick_best_token).
func pickBestToken(candidates []string, minTTL int64) string {
	if len(candidates) == 0 {
		return ""
	}
	var fresh []string
	for _, t := range candidates {
		if IsFreshToken(t, minTTL) {
			fresh = append(fresh, t)
		}
	}
	pool := fresh
	if len(pool) == 0 {
		for _, t := range candidates {
			if LooksLikeJWT(t) {
				pool = append(pool, t)
			}
		}
	}
	if len(pool) == 0 {
		return ""
	}

	var likely []string
	for _, t := range pool {
		if IsLikelyLeonardoToken(t) {
			likely = append(likely, t)
		}
	}
	if len(likely) > 0 {
		pool = likely
	}

	rank := func(t string) (int, int64) {
		payload := DecodeJWTPayload(t)
		var tokenUse string
		if payload != nil {
			tokenUse, _ = payload["token_use"].(string)
		}
		useScore := 1
		switch strings.ToLower(tokenUse) {
		case "access":
			useScore = 3
		case "id":
			useScore = 2
		}
		exp := TokenExp(t)
		if exp == 0 {
			exp = time.Now().Unix() + 120
		}
		return useScore, exp
	}

	best := pool[0]
	bestUse, bestExp := rank(best)
	for _, t := range pool[1:] {
		u, e := rank(t)
		if u > bestUse || (u == bestUse && e > bestExp) {
			best, bestUse, bestExp = t, u, e
		}
	}
	return best
}

// findTokenInObject recursively walks a JSON-like value looking for plausible JWTs.
func findTokenInObject(node any) string {
	var candidates []string
	add := func(raw any) {
		token := normalizeTokenCandidate(raw)
		if token != "" && LooksLikeJWT(token) {
			candidates = append(candidates, token)
		}
	}

	knownPaths := [][]string{
		{"accessToken"},
		{"access_token"},
		{"idToken"},
		{"id_token"},
		{"token"},
		{"user", "accessToken"},
		{"user", "access_token"},
		{"user", "idToken"},
		{"user", "id_token"},
		{"session", "accessToken"},
		{"session", "idToken"},
	}

	var walk func(any)
	walk = func(n any) {
		switch v := n.(type) {
		case string:
			add(v)
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			for _, path := range knownPaths {
				cur := any(v)
				ok := true
				for _, key := range path {
					obj, isMap := cur.(map[string]any)
					if !isMap {
						ok = false
						break
					}
					next, exists := obj[key]
					if !exists {
						ok = false
						break
					}
					cur = next
				}
				if ok {
					add(cur)
				}
			}
			for k, value := range v {
				lower := strings.ToLower(k)
				if strings.Contains(lower, "cf_access_token") {
					continue
				}
				switch lower {
				case "idtoken", "accesstoken", "id_token", "access_token", "token":
					walk(value)
					continue
				}
				if strings.Contains(lower, "token") {
					walk(value)
					continue
				}
				switch value.(type) {
				case map[string]any, []any:
					walk(value)
				}
			}
		}
	}

	walk(node)
	return pickBestToken(candidates, 120)
}

// fetchBetterAuthSession hits app.leonardo.ai/api/auth/get-session with
// Chrome TLS impersonation. Returns nil on failure (callers fall through
// to legacy strategies).
func (c *Client) fetchBetterAuthSession(cookieStr string) map[string]any {
	resp, err := c.impersonatorClient.R().
		SetHeader("cookie", cookieStr).
		SetHeader("accept", "application/json").
		SetHeader("accept-language", "en-US,en;q=0.9").
		SetHeader("origin", "https://app.leonardo.ai").
		SetHeader("referer", "https://app.leonardo.ai/").
		Get("https://app.leonardo.ai/api/auth/get-session")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil || len(body) == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil
	}
	return out
}

// GetTokenFromCookie resolves a usable Bearer JWT from a full cookie string.
//
//  1. better-auth /api/auth/get-session via Chrome TLS impersonation.
//  2. Legacy next-auth flow (POST with csrfToken, then GET).
//  3. Direct JWT extraction from session cookie value.
func (c *Client) GetTokenFromCookie(cookieStr string) string {
	// 1. Better-auth.
	session := c.fetchBetterAuthSession(cookieStr)
	if session != nil {
		if token := findTokenInObject(session); token != "" && IsFreshToken(token, 120) {
			return token
		}
	}

	// 2. Legacy next-auth via api.leonardo.ai (no TLS impersonation needed).
	headers := c.baseHeaders()
	headers["cookie"] = cookieStr

	csrf := ""
	for _, name := range []string{
		"__Host-next-auth.csrf-token",
		"__Secure-next-auth.csrf-token",
		"next-auth.csrf-token",
		"__Host-authjs.csrf-token",
		"__Secure-authjs.csrf-token",
		"authjs.csrf-token",
	} {
		raw := extractCookieValue(cookieStr, name)
		if raw != "" {
			decoded, err := url.QueryUnescape(raw)
			if err != nil {
				decoded = raw
			}
			csrf = strings.SplitN(decoded, "|", 2)[0]
			break
		}
	}

	tryJSON := func(method, body string) map[string]any {
		r := c.gqlClient.R().SetHeaders(headers)
		if body != "" {
			r.SetBodyJsonString(body)
		}
		var resp *req.Response
		var err error
		if method == "POST" {
			resp, err = r.Post(authURL + "/session")
		} else {
			resp, err = r.Get(authURL + "/session")
		}
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		if !resp.IsSuccessState() {
			return nil
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil || len(raw) == 0 {
			return nil
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil
		}
		return out
	}

	if csrf != "" {
		body, _ := json.Marshal(map[string]string{"csrfToken": csrf})
		if resp := tryJSON("POST", string(body)); resp != nil {
			if token := findTokenInObject(resp); token != "" && IsFreshToken(token, 120) {
				return token
			}
		}
	}

	if resp := tryJSON("GET", ""); resp != nil {
		if token := findTokenInObject(resp); token != "" && IsFreshToken(token, 120) {
			return token
		}
	}

	// 3. Direct JWT-in-cookie fallback.
	for _, name := range []string{
		"__Secure-next-auth.session-token",
		"next-auth.session-token",
		"__Secure-authjs.session-token",
		"authjs.session-token",
	} {
		raw := extractCookieValue(cookieStr, name)
		token := normalizeTokenCandidate(raw)
		if token != "" && IsFreshToken(token, 120) {
			return token
		}
	}

	return ""
}

// ErrNoUserDetails is returned when GraphQL responds without user_details data.
var ErrNoUserDetails = errors.New("leonardo: empty user_details")
