package store

import (
	"encoding/json"
	"fmt"
)

// QueueJobRow is one row of the queue_jobs table. String-slice fields are
// stored as JSON arrays in TEXT columns, mirroring generation_logs.
type QueueJobRow struct {
	ID           int64
	Type         string // "image" | "video"
	Status       string // pending | running | completed | failed | canceled
	Prompt       string
	ModelID      string // image modelId / video slug
	AspectRatio  string
	Resolution   string // video only
	Duration     int    // video only
	Audio        int    // video only (0/1)
	Quantity     int    // image only
	RefImageIDs  []string
	ResultURLs   []string
	ThumbURLs    []string
	UsedCookieID int64
	GenerationID string
	ErrorMessage string
	CreatedAt    int64
	UpdatedAt    int64
}

// AddQueueJob inserts a new job (typically status "pending") and returns its id.
func (s *Store) AddQueueJob(j QueueJobRow) (int64, error) {
	refJSON, err := json.Marshal(emptyOnNil(j.RefImageIDs))
	if err != nil {
		return 0, err
	}
	resJSON, err := json.Marshal(emptyOnNil(j.ResultURLs))
	if err != nil {
		return 0, err
	}
	thumbJSON, err := json.Marshal(emptyOnNil(j.ThumbURLs))
	if err != nil {
		return 0, err
	}

	ts := nowTS()
	if j.CreatedAt == 0 {
		j.CreatedAt = ts
	}
	j.UpdatedAt = ts

	res, err := s.db.Exec(
		`INSERT INTO queue_jobs (
			type, status, prompt, model_id, aspect_ratio, resolution,
			duration, audio, quantity, ref_image_ids_json, result_urls_json,
			thumb_urls_json, used_cookie_id, generation_id, error_message,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.Type, j.Status, j.Prompt, j.ModelID, j.AspectRatio, j.Resolution,
		j.Duration, j.Audio, j.Quantity, string(refJSON), string(resJSON),
		string(thumbJSON), j.UsedCookieID, j.GenerationID, truncate(j.ErrorMessage, 400),
		j.CreatedAt, j.UpdatedAt,
	)
	if err != nil {
		return 0, fmt.Errorf("store: add queue job: %w", err)
	}
	return res.LastInsertId()
}

// UpdateQueueJob overwrites the mutable fields of an existing job and bumps
// updated_at. The job is matched by ID.
func (s *Store) UpdateQueueJob(j QueueJobRow) error {
	resJSON, err := json.Marshal(emptyOnNil(j.ResultURLs))
	if err != nil {
		return err
	}
	thumbJSON, err := json.Marshal(emptyOnNil(j.ThumbURLs))
	if err != nil {
		return err
	}
	refJSON, err := json.Marshal(emptyOnNil(j.RefImageIDs))
	if err != nil {
		return err
	}

	_, err = s.db.Exec(
		`UPDATE queue_jobs SET
			status = ?, ref_image_ids_json = ?, result_urls_json = ?,
			thumb_urls_json = ?, used_cookie_id = ?, generation_id = ?,
			error_message = ?, updated_at = ?
		 WHERE id = ?`,
		j.Status, string(refJSON), string(resJSON), string(thumbJSON),
		j.UsedCookieID, j.GenerationID, truncate(j.ErrorMessage, 400), nowTS(),
		j.ID,
	)
	if err != nil {
		return fmt.Errorf("store: update queue job %d: %w", j.ID, err)
	}
	return nil
}

// ListQueueJobs returns all jobs, oldest first (FIFO order for processing and
// a stable display order).
func (s *Store) ListQueueJobs() ([]QueueJobRow, error) {
	rows, err := s.db.Query(
		`SELECT id, type, status, prompt, model_id, aspect_ratio, resolution,
		        duration, audio, quantity, ref_image_ids_json, result_urls_json,
		        thumb_urls_json, used_cookie_id, generation_id, error_message,
		        created_at, updated_at
		 FROM queue_jobs ORDER BY id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("store: list queue jobs: %w", err)
	}
	defer rows.Close()

	var out []QueueJobRow
	for rows.Next() {
		var (
			j                          QueueJobRow
			refJSON, resJSON, thumbJSON string
		)
		if err := rows.Scan(
			&j.ID, &j.Type, &j.Status, &j.Prompt, &j.ModelID, &j.AspectRatio, &j.Resolution,
			&j.Duration, &j.Audio, &j.Quantity, &refJSON, &resJSON,
			&thumbJSON, &j.UsedCookieID, &j.GenerationID, &j.ErrorMessage,
			&j.CreatedAt, &j.UpdatedAt,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(refJSON), &j.RefImageIDs)
		_ = json.Unmarshal([]byte(resJSON), &j.ResultURLs)
		_ = json.Unmarshal([]byte(thumbJSON), &j.ThumbURLs)
		out = append(out, j)
	}
	return out, rows.Err()
}

// DeleteQueueJob removes a single job by id.
func (s *Store) DeleteQueueJob(id int64) error {
	_, err := s.db.Exec("DELETE FROM queue_jobs WHERE id = ?", id)
	return err
}

// DeleteFinishedQueueJobs removes only terminal jobs (completed/failed/
// canceled), leaving pending/running untouched. Returns rows deleted.
func (s *Store) DeleteFinishedQueueJobs() (int64, error) {
	res, err := s.db.Exec(
		"DELETE FROM queue_jobs WHERE status IN ('completed', 'failed', 'canceled')",
	)
	if err != nil {
		return 0, fmt.Errorf("store: clear finished queue jobs: %w", err)
	}
	return res.RowsAffected()
}

// RequeueRunningJobs flips any job left in "running" (e.g. after an unclean
// shutdown) back to "pending" so it can be dispatched again on resume.
// Returns rows affected.
func (s *Store) RequeueRunningJobs() (int64, error) {
	res, err := s.db.Exec(
		"UPDATE queue_jobs SET status = 'pending', updated_at = ? WHERE status = 'running'",
		nowTS(),
	)
	if err != nil {
		return 0, fmt.Errorf("store: requeue running jobs: %w", err)
	}
	return res.RowsAffected()
}

func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max]
	}
	return s
}
