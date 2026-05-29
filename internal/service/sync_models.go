package service

import (
	"fmt"
	"strings"
)

// ModelSyncResult summarises a sync run for the UI.
type ModelSyncResult struct {
	Total   int      // models returned by Leonardo
	Added   int      // brand-new rows inserted
	Updated int      // existing rows refreshed
	Sample  []string // up to 5 names of newly added models, for toast display
}

// SyncImageModels pulls the official Leonardo model catalog via GraphQL and
// upserts it into the local `models` table. Mirrors the legacy fetch_models.py
// flow but lives entirely server-side so the UI can call it without exposing
// raw GraphQL.
//
// We rotate through active cookies, picking the first that resolves a usable
// token. Errors from one cookie don't abort the run.
func (p *LeonardoPool) SyncImageModels() (ModelSyncResult, error) {
	cookies, err := p.store.ListActiveCookies()
	if err != nil {
		return ModelSyncResult{}, fmt.Errorf("service: list cookies: %w", err)
	}
	if len(cookies) == 0 {
		return ModelSyncResult{}, newPublicError(400, "No active cookie. Add one in Cookies tab first.")
	}

	var token string
	for _, c := range cookies {
		if p.shouldSkipCookieNow(c) {
			continue
		}
		if t := p.resolveToken(c.Value); t != "" {
			token = t
			break
		}
	}
	if token == "" {
		return ModelSyncResult{}, newPublicError(503, "No cookie able to resolve a fresh token")
	}

	models, err := p.api.FetchOfficialImageModels(token)
	if err != nil {
		return ModelSyncResult{}, fmt.Errorf("service: fetch models: %w", err)
	}

	res := ModelSyncResult{Total: len(models)}
	existing, err := p.store.ListModels()
	if err != nil {
		return ModelSyncResult{}, err
	}
	known := make(map[string]struct{}, len(existing))
	for _, m := range existing {
		known[strings.TrimSpace(m.ModelID)] = struct{}{}
	}

	for _, m := range models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = "Model " + id[:8]
		}
		if _, ok := known[id]; ok {
			// Refresh display name + sd_version on each sync so renames stick.
			if err := p.store.UpsertModel(name, id, m.SDVersion); err == nil {
				res.Updated++
			}
			continue
		}
		if err := p.store.UpsertModel(name, id, m.SDVersion); err == nil {
			res.Added++
			if len(res.Sample) < 5 {
				res.Sample = append(res.Sample, name)
			}
		}
	}

	return res, nil
}
