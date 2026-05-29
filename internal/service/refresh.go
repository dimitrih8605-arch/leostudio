package service

import (
	"fmt"
	"strings"
)

// RefreshResult mirrors the Python refresh_cookie_profiles return type.
type RefreshResult struct {
	Checked int
	OK      int
}

// RefreshCookieProfiles iterates every cookie (including disabled ones),
// resolves a fresh JWT, fetches the user profile, and updates email +
// balance + status.
//
// Behaviour matches Python refresh_cookie_profiles:
//   - Active cookies that fail token resolve are auto-disabled with
//     AUTH_EXPIRED. Inactive ones just get last_error updated.
//   - Active cookies with zero balance are auto-disabled with DEPLETED.
//   - Successful refreshes increment the OK counter.
func (p *LeonardoPool) RefreshCookieProfiles() (RefreshResult, error) {
	cookies, err := p.store.ListCookies()
	if err != nil {
		return RefreshResult{}, fmt.Errorf("service: list cookies: %w", err)
	}

	res := RefreshResult{}
	for _, c := range cookies {
		res.Checked++
		isActive := c.IsActive == 1

		token := p.resolveToken(c.Value)
		if token == "" {
			if isActive {
				p.disableCookie(c.ID, "AUTH_EXPIRED", "Failed to fetch token from cookie")
			} else {
				_ = p.store.MarkCookieError(c.ID, "Failed to fetch token from cookie")
			}
			_ = p.store.UpdateCookieProfile(c.ID, "", 0)
			continue
		}

		info, err := p.api.GetUserInfo(token)
		if err != nil {
			msg := err.Error()
			if isAuthError(msg) && isActive {
				p.disableCookie(c.ID, "AUTH_EXPIRED", msg)
			} else {
				_ = p.store.MarkCookieError(c.ID, msg)
			}
			continue
		}

		p.refreshFallbackToken(c.ID, c.Value, token)
		_ = p.store.UpdateCookieProfile(c.ID, info.Email, info.Tokens)

		if info.Tokens <= 0 && isActive {
			p.disableCookie(c.ID, "DEPLETED", "Token balance is empty")
			continue
		}

		_ = p.store.MarkCookieUsed(c.ID)
		res.OK++
	}

	return res, nil
}

// RefreshCookieSessions re-resolves the JWT for every cookie via TLS
// impersonation, similar to RefreshCookieProfiles but with stricter token
// validation. Mirrors refresh_cookie_sessions in the Python codebase.
func (p *LeonardoPool) RefreshCookieSessions() (RefreshResult, error) {
	cookies, err := p.store.ListCookies()
	if err != nil {
		return RefreshResult{}, fmt.Errorf("service: list cookies: %w", err)
	}

	res := RefreshResult{}
	for _, c := range cookies {
		res.Checked++
		isActive := c.IsActive == 1

		token := p.resolveToken(c.Value)
		if token == "" {
			msg := "Session refresh gagal: token tidak ditemukan"
			if isActive {
				p.disableCookie(c.ID, "AUTH_EXPIRED", msg)
			} else {
				_ = p.store.MarkCookieError(c.ID, msg)
			}
			continue
		}
		if strings.Count(token, ".") != 2 {
			msg := "Session refresh gagal: token bearer tidak valid"
			if isActive {
				p.disableCookie(c.ID, "AUTH_EXPIRED", msg)
			} else {
				_ = p.store.MarkCookieError(c.ID, msg)
			}
			continue
		}

		info, err := p.api.GetUserInfo(token)
		if err != nil {
			msg := fmt.Sprintf("Session refresh gagal: %s", err.Error())
			if isAuthError(msg) && isActive {
				p.disableCookie(c.ID, "AUTH_EXPIRED", msg)
			} else {
				_ = p.store.MarkCookieError(c.ID, msg)
			}
			continue
		}

		p.refreshFallbackToken(c.ID, c.Value, token)
		_ = p.store.UpdateCookieProfile(c.ID, info.Email, info.Tokens)

		if info.Tokens <= 0 && isActive {
			p.disableCookie(c.ID, "DEPLETED", "Token balance is empty")
			continue
		}

		_ = p.store.MarkCookieUsed(c.ID)
		res.OK++
	}

	return res, nil
}
