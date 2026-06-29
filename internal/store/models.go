package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ListModels returns all model rows ordered by id desc.
func (s *Store) ListModels() ([]Model, error) {
	rows, err := s.db.Query("SELECT id, name, model_id, sd_version, is_default, created_at FROM models ORDER BY id DESC")
	if err != nil {
		return nil, fmt.Errorf("store: list models: %w", err)
	}
	defer rows.Close()

	var out []Model
	for rows.Next() {
		var m Model
		if err := rows.Scan(&m.ID, &m.Name, &m.ModelID, &m.SDVersion, &m.IsDefault, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddModel inserts a model. Falls back to a derived name when blank.
func (s *Store) AddModel(name, modelID string) error {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" && len(modelID) >= 8 {
		name = "Model " + modelID[:8]
	}
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO models (name, model_id, is_default, created_at) VALUES (?, ?, 0, ?)",
		name, modelID, nowTS(),
	)
	return err
}

// DeleteModel removes a row and ensures a default is still set.
func (s *Store) DeleteModel(id int64) error {
	if _, err := s.db.Exec("DELETE FROM models WHERE id = ?", id); err != nil {
		return err
	}
	var defaultID int64
	err := s.db.QueryRow("SELECT id FROM models WHERE is_default = 1 LIMIT 1").Scan(&defaultID)
	if errors.Is(err, sql.ErrNoRows) {
		var firstID int64
		if err := s.db.QueryRow("SELECT id FROM models ORDER BY id ASC LIMIT 1").Scan(&firstID); err == nil {
			_, err := s.db.Exec("UPDATE models SET is_default = 1 WHERE id = ?", firstID)
			return err
		}
		return nil
	}
	return err
}

// SetDefaultModel atomically promotes a single id to default.
func (s *Store) SetDefaultModel(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE models SET is_default = 0"); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.Exec("UPDATE models SET is_default = 1 WHERE id = ?", id); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// DefaultModelID returns the active default model_id (UUID), or empty.
func (s *Store) DefaultModelID() (string, error) {
	var id string
	err := s.db.QueryRow("SELECT model_id FROM models WHERE is_default = 1 LIMIT 1").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// GetModelByModelID looks up a row by external Leonardo UUID.
func (s *Store) GetModelByModelID(modelID string) (*Model, error) {
	row := s.db.QueryRow(
		"SELECT id, name, model_id, sd_version, is_default, created_at FROM models WHERE model_id = ? LIMIT 1",
		modelID,
	)
	var m Model
	err := row.Scan(&m.ID, &m.Name, &m.ModelID, &m.SDVersion, &m.IsDefault, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &m, err
}


// GetModelByName looks up a row by display name (case-insensitive).
func (s *Store) GetModelByName(name string) (*Model, error) {
	var m Model
	err := s.db.QueryRow(
		"SELECT id, name, model_id, sd_version, is_default, created_at FROM models WHERE LOWER(name) = LOWER(?)",
		name,
	).Scan(&m.ID, &m.Name, &m.ModelID, &m.SDVersion, &m.IsDefault, &m.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get model by name: %w", err)
	}
	return &m, nil
}

// GetSDVersion returns the sd_version associated with a model UUID, or "".
func (s *Store) GetSDVersion(modelID string) (string, error) {
	var sdVersion sql.NullString
	err := s.db.QueryRow("SELECT sd_version FROM models WHERE model_id = ? LIMIT 1", modelID).Scan(&sdVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return sdVersion.String, nil
}

// UpsertModel inserts or updates a model row, refreshing name and sdVersion.
// Used by the model sync flow so renames upstream propagate into the UI.
func (s *Store) UpsertModel(name, modelID, sdVersion string) error {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" && len(modelID) >= 8 {
		name = "Model " + modelID[:8]
	}
	sdVersion = strings.TrimSpace(sdVersion)
	if strings.EqualFold(sdVersion, "none") {
		sdVersion = ""
	}
	_, err := s.db.Exec(
		`INSERT INTO models (name, model_id, sd_version, is_default, created_at)
		 VALUES (?, ?, ?, 0, ?)
		 ON CONFLICT(model_id) DO UPDATE SET
		   name = excluded.name,
		   sd_version = excluded.sd_version`,
		name, modelID, sdVersion, nowTS(),
	)
	return err
}

// DeleteModelsNotIn deletes any model whose model_id is not in the keep list.
// Used by sync to purge stale rows that Leonardo no longer returns.
func (s *Store) DeleteModelsNotIn(keepIDs []string) error {
	if len(keepIDs) == 0 {
		return nil
	}
	placeholders := make([]string, len(keepIDs))
	args := make([]any, len(keepIDs))
	for i, id := range keepIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	_, err := s.db.Exec(
		"DELETE FROM models WHERE model_id NOT IN ("+strings.Join(placeholders, ",")+")",
		args...,
	)
	return err
}
