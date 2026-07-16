package server

// Model metadata: the "pretty" catalog data a provider publishes about its
// models — display name, description, knowledge cutoff, expiration.
//
// Source is GET {base_url}/models, nominally part of the OpenAI-compatible
// surface. Plain servers (llama.cpp, vLLM) return bare ids there; OpenRouter
// enriches the same envelope with name/description/knowledge_cutoff/
// expiration_date. All enrichment fields are optional, so JANUS uses them
// when present and falls back to the raw slug otherwise — no provider
// special-casing.
//
// This is display- and prompt-layer data only. It is cached in memory per
// endpoint (never persisted): it describes the provider's current catalog,
// not the agent, and yesterday's copy is worthless.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ModelInfo is one model's catalog entry, reduced to the fields JANUS uses.
type ModelInfo struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description"`
	ContextLength   int64  `json:"context_length"`
	KnowledgeCutoff string `json:"knowledge_cutoff"` // ISO date, "" if unpublished
	ExpirationDate  string `json:"expiration_date"`  // ISO date, "" = no announced retirement
}

const (
	metaTTL        = time.Hour        // how long a fetched catalog is trusted
	metaFailTTL    = 5 * time.Minute  // back-off after a failed fetch
	metaFetchLimit = 15 * time.Second // provider fetch timeout
)

type modelCatalog struct {
	fetched time.Time
	ok      bool // false = negative cache entry (fetch failed)
	models  map[string]*ModelInfo
}

// modelMeta caches provider catalogs per endpoint.
type modelMeta struct {
	mu   sync.Mutex
	byEP map[int64]*modelCatalog
}

func newModelMeta() *modelMeta {
	return &modelMeta{byEP: make(map[int64]*modelCatalog)}
}

// Lookup returns catalog metadata for a model slug on an endpoint, or nil if
// the endpoint publishes none (bare OpenAI-compatible servers, stubs, fetch
// failures). Callers must treat nil as "use the slug verbatim".
func (m *modelMeta) Lookup(e *Endpoint, slug string) *ModelInfo {
	if e == nil || e.Kind != "openai" || slug == "" {
		return nil
	}
	cat := m.catalog(e)
	if cat == nil {
		return nil
	}
	if mi, ok := cat.models[slug]; ok {
		return mi
	}
	// Routed slugs sometimes carry a variant suffix (":free", ":nitro")
	// that the catalog lists separately or not at all.
	if i := strings.LastIndex(slug, ":"); i > 0 {
		if mi, ok := cat.models[slug[:i]]; ok {
			return mi
		}
	}
	return nil
}

func (m *modelMeta) catalog(e *Endpoint) *modelCatalog {
	m.mu.Lock()
	cat := m.byEP[e.ID]
	if cat != nil {
		ttl := metaTTL
		if !cat.ok {
			ttl = metaFailTTL
		}
		if time.Since(cat.fetched) < ttl {
			m.mu.Unlock()
			if !cat.ok {
				return nil
			}
			return cat
		}
	}
	m.mu.Unlock()

	// Fetch outside the lock: a slow provider must not freeze every page
	// that wants a label. Concurrent fetches of the same catalog are
	// harmless (last one wins).
	fresh := fetchCatalog(e)
	m.mu.Lock()
	m.byEP[e.ID] = fresh
	m.mu.Unlock()
	if !fresh.ok {
		return nil
	}
	return fresh
}

func fetchCatalog(e *Endpoint) *modelCatalog {
	fail := &modelCatalog{fetched: time.Now(), ok: false}

	url := strings.TrimRight(e.BaseURL, "/") + "/models"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fail
	}
	if e.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.APIKey)
	}
	client := &http.Client{Timeout: metaFetchLimit}
	resp, err := client.Do(req)
	if err != nil {
		return fail
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail
	}

	var envelope struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return fail
	}

	models := make(map[string]*ModelInfo, len(envelope.Data))
	for _, raw := range envelope.Data {
		mi := &ModelInfo{}
		// Providers disagree on field types (dates as strings vs null,
		// etc.); decode leniently and keep whatever parses.
		var loose struct {
			ID              string `json:"id"`
			Name            string `json:"name"`
			Description     string `json:"description"`
			ContextLength   int64  `json:"context_length"`
			KnowledgeCutoff any    `json:"knowledge_cutoff"`
			ExpirationDate  any    `json:"expiration_date"`
		}
		if err := json.Unmarshal(raw, &loose); err != nil || loose.ID == "" {
			continue
		}
		mi.ID = loose.ID
		mi.Name = loose.Name
		mi.Description = loose.Description
		mi.ContextLength = loose.ContextLength
		mi.KnowledgeCutoff = looseDate(loose.KnowledgeCutoff)
		mi.ExpirationDate = looseDate(loose.ExpirationDate)
		models[mi.ID] = mi
	}
	if len(models) == 0 {
		return fail
	}
	return &modelCatalog{fetched: time.Now(), ok: true, models: models}
}

// looseDate renders a provider's date field, whatever type it arrived as.
func looseDate(v any) string {
	switch d := v.(type) {
	case nil:
		return ""
	case string:
		return d
	case float64: // unix seconds
		return time.Unix(int64(d), 0).UTC().Format("2006-01-02")
	default:
		return fmt.Sprintf("%v", d)
	}
}
