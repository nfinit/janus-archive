package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const richCatalog = `{"data":[
	{"id":"poolside/laguna-m.1","name":"Poolside: Laguna M.1",
	 "description":"Flagship coding agent model.","context_length":262144,
	 "knowledge_cutoff":"2025-11-01","expiration_date":null},
	{"id":"tencent/hy3:free","name":"Tencent: HY3 (free)",
	 "description":"A reasoning model.","expiration_date":"2026-07-21"}
]}`

// bareCatalog is what a plain OpenAI-compatible server returns: ids only.
const bareCatalog = `{"data":[{"id":"local-model","object":"model","created":1700000000,"owned_by":"me"}]}`

func metaServer(t *testing.T, body string, status int) (*httptest.Server, *Endpoint) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts, &Endpoint{ID: 1, Name: "test", Kind: "openai", BaseURL: ts.URL}
}

func TestModelMetaLookup(t *testing.T) {
	_, ep := metaServer(t, richCatalog, 200)
	m := newModelMeta()

	mi := m.Lookup(ep, "poolside/laguna-m.1")
	if mi == nil {
		t.Fatal("expected metadata for known slug")
	}
	if mi.Name != "Poolside: Laguna M.1" || mi.KnowledgeCutoff != "2025-11-01" {
		t.Errorf("bad metadata: %+v", mi)
	}
	if mi.ExpirationDate != "" {
		t.Errorf("null expiration_date should read as empty, got %q", mi.ExpirationDate)
	}

	if mi := m.Lookup(ep, "tencent/hy3:free"); mi == nil || mi.ExpirationDate != "2026-07-21" {
		t.Errorf("expiration date not surfaced: %+v", mi)
	}

	// Variant-suffix fallback: a served slug with an unlisted variant should
	// resolve to its base entry.
	if mi := m.Lookup(ep, "poolside/laguna-m.1:nitro"); mi == nil || mi.Name != "Poolside: Laguna M.1" {
		t.Errorf("variant suffix fallback failed: %+v", mi)
	}

	if mi := m.Lookup(ep, "no/such-model"); mi != nil {
		t.Errorf("unknown slug should return nil, got %+v", mi)
	}
}

func TestModelMetaBareServer(t *testing.T) {
	// A plain OpenAI-compatible catalog has no enrichment fields; entries
	// still resolve, and empty names must fall back to the slug at the
	// display layer (Lookup returns the entry; Name is empty).
	_, ep := metaServer(t, bareCatalog, 200)
	m := newModelMeta()
	mi := m.Lookup(ep, "local-model")
	if mi == nil {
		t.Fatal("bare catalog entries should still resolve")
	}
	if mi.Name != "" || mi.Description != "" {
		t.Errorf("bare entry should have no enrichment: %+v", mi)
	}
}

func TestModelMetaFailureAndNonOpenAI(t *testing.T) {
	_, ep := metaServer(t, "oops", 500)
	m := newModelMeta()
	if mi := m.Lookup(ep, "anything"); mi != nil {
		t.Errorf("failed fetch should yield nil, got %+v", mi)
	}
	// The failure is negatively cached: a second lookup must not hang or
	// panic (and still returns nil).
	if mi := m.Lookup(ep, "anything"); mi != nil {
		t.Errorf("negative cache lookup should yield nil, got %+v", mi)
	}

	stub := &Endpoint{ID: 2, Kind: "stub"}
	if mi := m.Lookup(stub, "stub"); mi != nil {
		t.Errorf("stub endpoints have no catalog, got %+v", mi)
	}
	if mi := m.Lookup(nil, "x"); mi != nil {
		t.Error("nil endpoint must be safe")
	}
}
