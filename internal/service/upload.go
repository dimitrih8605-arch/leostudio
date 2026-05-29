package service

import (
	"fmt"
	"log"
	"strings"
)

// UploadLocalImage uploads raw bytes (e.g. drag-drop file) and returns the
// init image id for use as a reference / start frame. It rotates through the
// active cookie pool exactly like Generate does so the upload is always
// signed with a working JWT.
func (p *LeonardoPool) UploadLocalImage(content []byte, ext string) (string, error) {
	if len(content) == 0 {
		return "", fmt.Errorf("empty image bytes")
	}
	cookies, err := p.store.ListActiveCookies()
	if err != nil {
		return "", err
	}
	if len(cookies) == 0 {
		return "", newPublicError(400, "No active cookie configured")
	}

	var lastErr string
	for _, cookie := range cookies {
		if p.shouldSkipCookieNow(cookie) {
			continue
		}
		token := p.resolveToken(cookie.Value)
		if token == "" {
			log.Printf("[upload] cookie#%d: token resolve failed", cookie.ID)
			continue
		}
		log.Printf("[upload] cookie#%d: starting Leonardo upload (%d bytes, ext=%s)", cookie.ID, len(content), ext)
		id, err := p.api.UploadImageBytes(token, content, ext)
		if err != nil {
			msg := err.Error()
			log.Printf("[upload] cookie#%d: failed: %s", cookie.ID, msg)
			if isAuthError(msg) {
				p.disableCookie(cookie.ID, "AUTH_EXPIRED", msg)
			}
			lastErr = msg
			continue
		}
		_ = p.store.MarkCookieUsed(cookie.ID)
		log.Printf("[upload] cookie#%d: success id=%s", cookie.ID, id)
		return id, nil
	}

	if lastErr == "" {
		lastErr = "all cookies failed to upload"
	}
	return "", newPublicError(503, "Upload failed: "+strings.TrimSpace(lastErr))
}
