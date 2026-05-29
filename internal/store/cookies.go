package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ListCookies returns all cookies ordered by id desc (matches Python).
func (s *Store) ListCookies() ([]Cookie, error) {
	return s.queryCookies("SELECT * FROM cookies ORDER BY id DESC")
}

// ListActiveCookies returns active cookies ordered by least-recently-used.
func (s *Store) ListActiveCookies() ([]Cookie, error) {
	return s.queryCookies(
		"SELECT * FROM cookies WHERE is_active = 1 ORDER BY last_used_at ASC, id ASC",
	)
}

// AddCookie inserts a cookie value. Idempotent on the unique constraint.
func (s *Store) AddCookie(value string) error {
	v := strings.TrimSpace(value)
	if v == "" {
		return nil
	}
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO cookies (value, is_active, created_at) VALUES (?, 1, ?)",
		v, nowTS(),
	)
	return err
}

// GetCookieByValue returns the row matching the literal cookie payload.
func (s *Store) GetCookieByValue(value string) (*Cookie, error) {
	rows, err := s.queryCookies("SELECT * FROM cookies WHERE value = ? LIMIT 1", value)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
}

// UpdateCookieValue replaces the cookie payload for an existing id.
// Returns false if no change occurred or the new value collides.
func (s *Store) UpdateCookieValue(id int64, value string) (bool, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return false, nil
	}

	var current string
	err := s.db.QueryRow("SELECT value FROM cookies WHERE id = ? LIMIT 1", id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: load cookie %d: %w", id, err)
	}
	if strings.TrimSpace(current) == v {
		return false, nil
	}

	if _, err := s.db.Exec("UPDATE cookies SET value = ? WHERE id = ?", v, id); err != nil {
		// Silently swallow uniqueness collisions like the Python version.
		if strings.Contains(err.Error(), "UNIQUE") {
			return false, nil
		}
		return false, fmt.Errorf("store: update cookie %d: %w", id, err)
	}
	return true, nil
}

// DeleteCookie removes a cookie row.
func (s *Store) DeleteCookie(id int64) error {
	_, err := s.db.Exec("DELETE FROM cookies WHERE id = ?", id)
	return err
}

// ToggleCookie flips the is_active flag and clears disabled metadata when re-enabled.
func (s *Store) ToggleCookie(id int64, enabled bool) error {
	if enabled {
		_, err := s.db.Exec(
			"UPDATE cookies SET is_active = 1, disabled_reason = '', disabled_at = 0 WHERE id = ?",
			id,
		)
		return err
	}
	_, err := s.db.Exec("UPDATE cookies SET is_active = 0 WHERE id = ?", id)
	return err
}

// AutoDisableCookie disables a cookie with a structured reason.
func (s *Store) AutoDisableCookie(id int64, reason string) error {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(reason), " ", "_"))
	if normalized == "" {
		normalized = "AUTO_DISABLED"
	}
	if len(normalized) > 64 {
		normalized = normalized[:64]
	}
	_, err := s.db.Exec(
		"UPDATE cookies SET is_active = 0, disabled_reason = ?, disabled_at = ? WHERE id = ?",
		normalized, nowTS(), id,
	)
	return err
}

// MarkCookieUsed records a successful usage timestamp and clears errors.
func (s *Store) MarkCookieUsed(id int64) error {
	now := nowTS()
	_, err := s.db.Exec(
		"UPDATE cookies SET last_used_at = ?, last_checked_at = ?, last_error = '' WHERE id = ?",
		now, now, id,
	)
	return err
}

// MarkCookieError stores the latest failure reason.
func (s *Store) MarkCookieError(id int64, message string) error {
	if len(message) > 300 {
		message = message[:300]
	}
	_, err := s.db.Exec(
		"UPDATE cookies SET last_error = ?, last_checked_at = ? WHERE id = ?",
		message, nowTS(), id,
	)
	return err
}

// UpdateCookieProfile updates email + balance after a profile fetch.
func (s *Store) UpdateCookieProfile(id int64, email string, balance int64) error {
	if len(email) > 200 {
		email = email[:200]
	}
	_, err := s.db.Exec(
		"UPDATE cookies SET email = ?, last_balance = ?, last_checked_at = ? WHERE id = ?",
		email, balance, nowTS(), id,
	)
	return err
}

func (s *Store) queryCookies(query string, args ...any) ([]Cookie, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query cookies: %w", err)
	}
	defer rows.Close()

	var (
		out  []Cookie
		cols []string
	)
	cols, err = rows.Columns()
	if err != nil {
		return nil, err
	}

	for rows.Next() {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}

		var c Cookie
		for i, name := range cols {
			switch name {
			case "id":
				c.ID = toInt64(raw[i])
			case "value":
				c.Value = toString(raw[i])
			case "is_active":
				c.IsActive = int(toInt64(raw[i]))
			case "last_error":
				c.LastError = toString(raw[i])
			case "last_used_at":
				c.LastUsedAt = toInt64(raw[i])
			case "email":
				c.Email = toString(raw[i])
			case "last_balance":
				c.LastBalance = toInt64(raw[i])
			case "last_checked_at":
				c.LastCheckedAt = toInt64(raw[i])
			case "disabled_reason":
				c.DisabledReason = toString(raw[i])
			case "disabled_at":
				c.DisabledAt = toInt64(raw[i])
			case "created_at":
				c.CreatedAt = toInt64(raw[i])
			}
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func toInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case []byte:
		var n int64
		fmt.Sscanf(string(t), "%d", &n)
		return n
	case string:
		var n int64
		fmt.Sscanf(t, "%d", &n)
		return n
	}
	return 0
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	}
	return ""
}
