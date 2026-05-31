// Package store provides SQLite persistence for cookies, models, settings,
// and generation logs. It mirrors the Python `app/store.py` behaviour so
// existing data/app.db files keep working.
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store wraps a SQLite connection.
type Store struct {
	db *sql.DB
}

// Cookie represents a row from the `cookies` table.
type Cookie struct {
	ID             int64
	Value          string
	IsActive       int
	LastError      string
	LastUsedAt     int64
	Email          string
	LastBalance    int64
	LastCheckedAt  int64
	DisabledReason string
	DisabledAt     int64
	CreatedAt      int64
}

// Model represents a row from the `models` table.
type Model struct {
	ID        int64
	Name      string
	ModelID   string
	SDVersion string
	IsDefault int
	CreatedAt int64
}

// Open initialises the database file and runs schema migrations.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("store: empty path")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("store: create dir: %w", err)
		}
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(30000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite + WAL works best with a single writer.

	s := &Store{db: db}
	if err := s.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// HashPassword returns the same SHA-256 hex digest as the Python version.
func HashPassword(password string) string {
	sum := sha256.Sum256([]byte(password))
	return hex.EncodeToString(sum[:])
}

func nowTS() int64 {
	return time.Now().Unix()
}

func (s *Store) initSchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS admin_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS cookies (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			value TEXT UNIQUE NOT NULL,
			is_active INTEGER NOT NULL DEFAULT 1,
			last_error TEXT DEFAULT '',
			last_used_at INTEGER DEFAULT 0,
			email TEXT DEFAULT '',
			last_balance INTEGER DEFAULT 0,
			last_checked_at INTEGER DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS models (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			model_id TEXT UNIQUE NOT NULL,
			sd_version TEXT NOT NULL DEFAULT '',
			is_default INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS generation_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			provider_generation_id TEXT,
			used_cookie_id INTEGER,
			model_id TEXT,
			aspect_ratio TEXT,
			prompt TEXT,
			image_urls_json TEXT DEFAULT '[]',
			saved_files_json TEXT DEFAULT '[]',
			save_enabled INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'success',
			error_message TEXT DEFAULT '',
			created_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS queue_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL,
			status TEXT NOT NULL,
			prompt TEXT NOT NULL,
			model_id TEXT DEFAULT '',
			aspect_ratio TEXT DEFAULT '',
			resolution TEXT DEFAULT '',
			duration INTEGER DEFAULT 0,
			audio INTEGER DEFAULT 0,
			quantity INTEGER DEFAULT 1,
			ref_image_ids_json TEXT DEFAULT '[]',
			result_urls_json TEXT DEFAULT '[]',
			thumb_urls_json TEXT DEFAULT '[]',
			used_cookie_id INTEGER DEFAULT 0,
			generation_id TEXT DEFAULT '',
			error_message TEXT DEFAULT '',
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("store: init schema: %w", err)
		}
	}
	if err := s.ensureCookieColumns(); err != nil {
		return err
	}
	if err := s.ensureModelColumns(); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureCookieColumns() error {
	cols, err := s.tableColumns("cookies")
	if err != nil {
		return err
	}
	add := func(col, def string) error {
		if _, ok := cols[col]; ok {
			return nil
		}
		_, err := s.db.Exec(fmt.Sprintf("ALTER TABLE cookies ADD COLUMN %s %s", col, def))
		return err
	}
	for _, ent := range []struct {
		col string
		def string
	}{
		{"email", "TEXT DEFAULT ''"},
		{"last_balance", "INTEGER DEFAULT 0"},
		{"last_checked_at", "INTEGER DEFAULT 0"},
		{"disabled_reason", "TEXT DEFAULT ''"},
		{"disabled_at", "INTEGER DEFAULT 0"},
	} {
		if err := add(ent.col, ent.def); err != nil {
			return fmt.Errorf("store: alter cookies %s: %w", ent.col, err)
		}
	}
	return nil
}

func (s *Store) ensureModelColumns() error {
	cols, err := s.tableColumns("models")
	if err != nil {
		return err
	}
	if _, ok := cols["sd_version"]; !ok {
		if _, err := s.db.Exec("ALTER TABLE models ADD COLUMN sd_version TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("store: alter models sd_version: %w", err)
		}
	}
	return nil
}

func (s *Store) tableColumns(table string) (map[string]struct{}, error) {
	rows, err := s.db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return nil, fmt.Errorf("store: pragma %s: %w", table, err)
	}
	defer rows.Close()

	out := map[string]struct{}{}
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out[name] = struct{}{}
	}
	return out, rows.Err()
}

// Bootstrap seeds default settings, ingests model_id.txt, and ensures
// at least one default model exists.
func (s *Store) Bootstrap(modelFile string) error {
	defaults := [][2]string{
		{"default_aspect_ratio", "1:1"},
		{"auto_save_images", "0"},
		{"save_images_dir", "data/generated"},
		{"telegram_enabled", "0"},
		{"telegram_bot_token", ""},
		{"telegram_allowed_chat_ids", ""},
		{"studio_enabled", "0"},
		{"studio_proxy_base_url", ""},
		{"studio_auth_username", "studio"},
		{"studio_auth_password_hash", HashPassword("studio123")},
	}
	for _, kv := range defaults {
		if err := s.ensureSetting(kv[0], kv[1]); err != nil {
			return err
		}
	}

	if modelFile != "" {
		if data, err := os.ReadFile(modelFile); err == nil {
			entries := parseModelsFile(string(data))
			now := nowTS()
			for _, e := range entries {
				_, err := s.db.Exec(
					`INSERT INTO models (name, model_id, sd_version, is_default, created_at)
					 VALUES (?, ?, ?, 0, ?)
					 ON CONFLICT(model_id) DO UPDATE SET
						name = excluded.name,
						sd_version = excluded.sd_version`,
					e.Name, e.ModelID, e.SDVersion, now,
				)
				if err != nil {
					return fmt.Errorf("store: insert model: %w", err)
				}
			}

			// Drop legacy non-uuid rows.
			if _, err := s.db.Exec(
				"DELETE FROM models WHERE model_id NOT GLOB '????????-????-????-????-????????????'",
			); err != nil {
				return fmt.Errorf("store: cleanup legacy models: %w", err)
			}
		}
	}

	// Ensure exactly one default exists.
	var defaultID int64
	err := s.db.QueryRow("SELECT id FROM models WHERE is_default = 1 LIMIT 1").Scan(&defaultID)
	if err == sql.ErrNoRows {
		var firstID int64
		row := s.db.QueryRow("SELECT id FROM models ORDER BY id ASC LIMIT 1")
		if err := row.Scan(&firstID); err == nil {
			if _, err := s.db.Exec("UPDATE models SET is_default = 1 WHERE id = ?", firstID); err != nil {
				return fmt.Errorf("store: set default model: %w", err)
			}
		}
	} else if err != nil {
		return fmt.Errorf("store: query default model: %w", err)
	}
	return nil
}

func (s *Store) ensureSetting(key, value string) error {
	_, err := s.db.Exec(
		"INSERT OR IGNORE INTO settings (key, value) VALUES (?, ?)",
		key, value,
	)
	return err
}

// modelEntry mirrors the tuple used by parse_models_file in Python.
type modelEntry struct {
	Name      string
	ModelID   string
	SDVersion string
}

var (
	uuidRE      = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	numberedRE  = regexp.MustCompile(`^\d+\.\s+(.+)$`)
	sdVersionRE = regexp.MustCompile(`sdVersion\s*=\s*([A-Za-z0-9_]+)`)
)

func parseModelsFile(content string) []modelEntry {
	var (
		entries     []modelEntry
		currentName string
		currentID   string
		currentSD   string
	)

	flush := func() {
		if currentID != "" && uuidRE.MatchString(currentID) {
			name := currentName
			if name == "" && len(currentID) >= 8 {
				name = "Model " + currentID[:8]
			}
			sd := currentSD
			if strings.EqualFold(sd, "none") {
				sd = ""
			}
			entries = append(entries, modelEntry{Name: name, ModelID: currentID, SDVersion: sd})
		}
		currentID = ""
		currentSD = ""
	}

	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := numberedRE.FindStringSubmatch(line); m != nil {
			flush()
			currentName = strings.TrimSpace(m[1])
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "id=") {
			currentID = strings.TrimSpace(strings.SplitN(line, "=", 2)[1])
			continue
		}
		if m := sdVersionRE.FindStringSubmatch(line); m != nil {
			currentSD = strings.TrimSpace(m[1])
			continue
		}
	}
	flush()
	return entries
}
