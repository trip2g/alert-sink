package alertsink

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTrip2g implements /_system/hat and /_system/graphql, keeping notes in
// memory with trip2g's updateNotes semantics (create-only sentinel, patch
// find/replace with uniqueness check).
type fakeTrip2g struct {
	mu       sync.Mutex
	notes    map[string]string
	releases int
}

func newFakeTrip2g() *fakeTrip2g {
	return &fakeTrip2g{notes: map[string]string{}}
}

func (f *fakeTrip2g) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /_system/hat", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("token") == "" {
			http.Error(w, "missing token", http.StatusBadRequest)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "sess-token"})
		w.WriteHeader(http.StatusFound)
	})

	mux.HandleFunc("POST /_system/graphql", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sess-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req struct {
			Query     string `json:"query"`
			Variables struct {
				Changes []NoteChange `json:"changes"`
			} `json:"variables"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			t.Errorf("decode graphql request: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if strings.Contains(req.Query, "createRelease") {
			f.mu.Lock()
			f.releases++
			f.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"admin": map[string]any{"createRelease": map[string]any{
					"__typename": "CreateReleasePayload",
					"release":    map[string]any{"id": 1},
				}}},
			})
			return
		}

		result := f.apply(req.Variables.Changes)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"updateNotes": result},
		})
	})

	return mux
}

// apply mirrors trip2g's updatenotes resolve semantics closely enough for
// the sink's behavior to be tested.
func (f *fakeTrip2g) apply(changes []NoteChange) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()

	var paths []string
	for _, ch := range changes {
		switch {
		case ch.Upsert != nil:
			up := ch.Upsert
			if up.ExpectedHash != nil {
				_, exists := f.notes[up.Path]
				// Empty expected hash is the create-only sentinel.
				if *up.ExpectedHash == "" && exists {
					return map[string]any{"__typename": "UpdateNotesHashMismatchPayload", "path": up.Path, "actualHash": "x"}
				}
			}
			f.notes[up.Path] = up.Content
			paths = append(paths, up.Path)
		case ch.Patch != nil:
			p := ch.Patch
			content, exists := f.notes[p.Path]
			if !exists {
				return map[string]any{"__typename": "ErrorPayload", "message": "note not found: " + p.Path}
			}
			if strings.Count(content, p.Find) != 1 {
				return map[string]any{"__typename": "UpdateNotesPatchNotFoundPayload", "path": p.Path, "find": p.Find}
			}
			f.notes[p.Path] = strings.Replace(content, p.Find, p.Replace, 1)
			paths = append(paths, p.Path)
		}
	}
	return map[string]any{"__typename": "UpdateNotesSuccessPayload", "paths": paths}
}

func (f *fakeTrip2g) note(path string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.notes[path]
	return c, ok
}

func (f *fakeTrip2g) set(path, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notes[path] = content
}

func (f *fakeTrip2g) releaseCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.releases
}

func newTestSink(t *testing.T, fake *fakeTrip2g) *Sink {
	srv := httptest.NewServer(fake.handler(t))
	t.Cleanup(srv.Close)

	cfg := Config{
		Trip2gURL:    srv.URL,
		JwtSecret:    "test-secret",
		Email:        "alert-sink@local",
		TelegramTags: []string{"incidents"},
		QueueSize:    10,
		Timeout:      5 * time.Second,
		AutoRelease:  true,
	}
	client := NewTrip2gClient(cfg.Trip2gURL, cfg.JwtSecret, cfg.Email, cfg.Timeout)
	return NewSink(cfg, client, &Metrics{})
}

func TestProcessFiringCreatesNoteAndIndex(t *testing.T) {
	fake := newFakeTrip2g()
	sink := newTestSink(t, fake)
	a := testAlert("firing")

	err := sink.process(context.Background(), a)
	if err != nil {
		t.Fatalf("process firing: %v", err)
	}

	content, ok := fake.note(NotePath(a))
	if !ok {
		t.Fatalf("incident note not written at %s; have %v", NotePath(a), fake.notes)
	}
	if !strings.Contains(content, "status: firing") {
		t.Errorf("note not firing:\n%s", content)
	}
	index, ok := fake.note(IndexPath)
	if !ok {
		t.Fatal("incidents/index.md not created")
	}
	if !strings.Contains(index, "magazine_sort_property: starts_at") {
		t.Errorf("index note malformed:\n%s", index)
	}
	if fake.releaseCount() != 1 {
		t.Errorf("release count = %d, want 1 (publish after write)", fake.releaseCount())
	}
}

func TestProcessRepeatFiringLeavesExistingNote(t *testing.T) {
	fake := newFakeTrip2g()
	sink := newTestSink(t, fake)
	a := testAlert("firing")

	// Note exists with an agent edit; a repeat firing webhook must not clobber it.
	edited := RenderNote(a, []string{"incidents"}) + "\nagent-added analysis line\n"
	fake.set(NotePath(a), edited)

	err := sink.process(context.Background(), a)
	if err != nil {
		t.Fatalf("process repeat firing: %v", err)
	}
	content, _ := fake.note(NotePath(a))
	if !strings.Contains(content, "agent-added analysis line") {
		t.Fatal("repeat firing clobbered agent edits")
	}
	if fake.releaseCount() != 0 {
		t.Errorf("release count = %d, want 0 (no-op write must not publish)", fake.releaseCount())
	}
}

func TestProcessResolvedPatchesStatusOnly(t *testing.T) {
	fake := newFakeTrip2g()
	sink := newTestSink(t, fake)

	firing := testAlert("firing")
	err := sink.process(context.Background(), firing)
	if err != nil {
		t.Fatalf("process firing: %v", err)
	}

	// An agent adds a postmortem link before resolution arrives.
	content, _ := fake.note(NotePath(firing))
	fake.set(NotePath(firing), content+"\npostmortem-link-added-by-agent\n")

	resolved := testAlert("resolved")
	err = sink.process(context.Background(), resolved)
	if err != nil {
		t.Fatalf("process resolved: %v", err)
	}

	got, _ := fake.note(NotePath(resolved))
	if !strings.Contains(got, "status: resolved\nends_at: \"2026-07-07T14:41:00Z\"") {
		t.Errorf("resolution not patched:\n%s", got)
	}
	if !strings.Contains(got, "postmortem-link-added-by-agent") {
		t.Error("resolution patch clobbered the agent's edit")
	}
	// Visible callout must flip; description and postmortem link must survive.
	if !strings.Contains(got, "> ✅ **RESOLVED**") {
		t.Error("resolved callout not present after patch")
	}
	if strings.Contains(got, "🔴") {
		t.Error("firing emoji still present after patch")
	}
	if !strings.Contains(got, "Prometheus cannot scrape node-02.") {
		t.Error("patch touched more than the status block (description changed)")
	}
}

func TestProcessResolvedWithoutFiringFallsBackToUpsert(t *testing.T) {
	fake := newFakeTrip2g()
	sink := newTestSink(t, fake)
	resolved := testAlert("resolved")

	err := sink.process(context.Background(), resolved)
	if err != nil {
		t.Fatalf("process resolved without firing: %v", err)
	}
	got, ok := fake.note(NotePath(resolved))
	if !ok {
		t.Fatal("resolved note not created on missed firing")
	}
	if !strings.Contains(got, "status: resolved") {
		t.Errorf("fallback note not resolved:\n%s", got)
	}
}

func TestProcessResolvedTwiceIsIdempotent(t *testing.T) {
	fake := newFakeTrip2g()
	sink := newTestSink(t, fake)

	err := sink.process(context.Background(), testAlert("firing"))
	if err != nil {
		t.Fatalf("process firing: %v", err)
	}
	resolved := testAlert("resolved")
	for i := range 2 {
		err = sink.process(context.Background(), resolved)
		if err != nil {
			t.Fatalf("process resolved #%d: %v", i+1, err)
		}
	}
	got, _ := fake.note(NotePath(resolved))
	if strings.Count(got, "status: resolved") != 1 {
		t.Errorf("re-delivered resolution corrupted the note:\n%s", got)
	}
}

func TestParseWebhook(t *testing.T) {
	body := `{
		"version": "4",
		"groupKey": "{}:{alertname=\"NodeDown\"}",
		"status": "firing",
		"receiver": "default",
		"alerts": [{
			"status": "firing",
			"labels": {"alertname": "NodeDown", "severity": "critical"},
			"annotations": {"summary": "down"},
			"startsAt": "2026-07-07T14:37:00Z",
			"endsAt": "0001-01-01T00:00:00Z",
			"fingerprint": "ab12cd34ef56"
		}]
	}`
	p, err := ParseWebhook(strings.NewReader(body))
	if err != nil {
		t.Fatalf("ParseWebhook: %v", err)
	}
	if len(p.Alerts) != 1 || p.Alerts[0].Name() != "NodeDown" {
		t.Fatalf("unexpected payload: %+v", p)
	}

	_, err = ParseWebhook(strings.NewReader(`{"version": "3", "alerts": []}`))
	if err == nil {
		t.Fatal("version 3 payload must be rejected")
	}
}
