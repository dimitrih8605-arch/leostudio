package store

import (
	"database/sql"
	"encoding/json"
	"errors"
)

// GetSetting returns the value for key or the default when missing.
func (s *Store) GetSetting(key, fallback string) (string, error) {
	var v string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return fallback, err
	}
	return v, nil
}

// SetSetting upserts a value for key.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		"INSERT INTO settings (key, value) VALUES (?, ?) "+
			"ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		key, value,
	)
	return err
}

// AddGenerationLog records a generation result for auditing.
func (s *Store) AddGenerationLog(
	providerGenerationID string,
	usedCookieID int64,
	modelID string,
	aspectRatio string,
	prompt string,
	imageURLs []string,
	savedFiles []string,
	saveEnabled bool,
	status string,
	errorMessage string,
) error {
	if len(errorMessage) > 400 {
		errorMessage = errorMessage[:400]
	}

	urlsJSON, err := json.Marshal(emptyOnNil(imageURLs))
	if err != nil {
		return err
	}
	savedJSON, err := json.Marshal(emptyOnNil(savedFiles))
	if err != nil {
		return err
	}

	saveFlag := 0
	if saveEnabled {
		saveFlag = 1
	}

	_, err = s.db.Exec(
		`INSERT INTO generation_logs (
			provider_generation_id,
			used_cookie_id,
			model_id,
			aspect_ratio,
			prompt,
			image_urls_json,
			saved_files_json,
			save_enabled,
			status,
			error_message,
			created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		providerGenerationID,
		usedCookieID,
		modelID,
		aspectRatio,
		prompt,
		string(urlsJSON),
		string(savedJSON),
		saveFlag,
		status,
		errorMessage,
		nowTS(),
	)
	return err
}

func emptyOnNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// GenerationLog is a single audit row used by the Library UI.
type GenerationLog struct {
	ID                   int64
	ProviderGenerationID string
	UsedCookieID         int64
	ModelID              string
	AspectRatio          string
	Prompt               string
	ImageURLsJSON        string
	SavedFilesJSON       string
	SaveEnabled          int
	Status               string
	ErrorMessage         string
	CreatedAt            int64
}

// ListGenerationLogs returns the most recent rows, newest first.
func (s *Store) ListGenerationLogs(limit int) ([]GenerationLog, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(
		`SELECT id, provider_generation_id, used_cookie_id, model_id, aspect_ratio,
		        prompt, image_urls_json, saved_files_json, save_enabled,
		        status, error_message, created_at
		 FROM generation_logs ORDER BY id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []GenerationLog
	for rows.Next() {
		var g GenerationLog
		if err := rows.Scan(
			&g.ID, &g.ProviderGenerationID, &g.UsedCookieID, &g.ModelID, &g.AspectRatio,
			&g.Prompt, &g.ImageURLsJSON, &g.SavedFilesJSON, &g.SaveEnabled,
			&g.Status, &g.ErrorMessage, &g.CreatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
