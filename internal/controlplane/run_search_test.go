package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/owainlewis/machinist/internal/protocol"
)

func TestSearchRunEventsTreatsQueryAsLiteralText(t *testing.T) {
	s, err := openFactoryLocalStore(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	ctx := context.Background()
	for run, events := range map[string][]string{
		"run":   {`{"sequence":1,"type":"agent.activity","text":"build passed"}`, `{"sequence":2,"type":"agent.activity","text":"100% done"}`, `{"sequence":3,"type":"agent.activity","text":"it's here"}`},
		"other": {`{"sequence":1,"type":"agent.activity","text":"build passed"}`},
	} {
		raw := []json.RawMessage{}
		for _, event := range events {
			raw = append(raw, json.RawMessage(event))
		}
		if _, err = s.ingestRunEvents(ctx, run, raw); err != nil {
			t.Fatal(err)
		}
	}
	server := &GitHubFactoryServer{local: s}
	for query, want := range map[string]int{
		"build":                    1,
		"%":                        1,
		"_":                        0,
		"it's":                     1,
		"' OR '1'='1":              0,
		"x' UNION SELECT 1,'y' --": 0,
	} {
		r := httptest.NewRequest("GET", "/?q="+urlQuery(query), nil)
		r.SetPathValue("id", "run")
		w := httptest.NewRecorder()
		server.searchRunEvents(w, r)
		var page protocol.RunEventPage
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Events) != want {
			t.Fatalf("query %q: status %d, body %s, want %d events", query, w.Code, w.Body.String(), want)
		}
	}
}

func TestLoopbackOnlyRejectsOtherHosts(t *testing.T) {
	handler := loopbackOnly(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	for host, want := range map[string]int{
		"127.0.0.1:7331":     204,
		"localhost:7331":     204,
		"[::1]:7331":         204,
		"evil.example:7331":  421,
		"127.0.0.1.nip.io":   421,
		"machinist.internal": 421,
	} {
		r := httptest.NewRequest("GET", "/api/v1/status", nil)
		r.Host = host
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("host %q: got %d, want %d", host, w.Code, want)
		}
	}
}

func urlQuery(text string) string {
	return (&url.URL{RawQuery: url.Values{"q": {text}}.Encode()}).RawQuery[2:]
}
