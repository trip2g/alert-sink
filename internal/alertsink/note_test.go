package alertsink

import (
	"strings"
	"testing"
	"time"
)

func testAlert(status string) Alert {
	starts := time.Date(2026, 7, 7, 14, 37, 0, 0, time.UTC)
	a := Alert{
		Status: status,
		Labels: map[string]string{
			"alertname": "NodeDown",
			"severity":  "critical",
			"instance":  "node-02:9100",
		},
		Annotations: map[string]string{
			"summary":     "Node exporter target is down.",
			"description": "Prometheus cannot scrape node-02.",
		},
		StartsAt:    starts,
		Fingerprint: "ab12cd34ef56",
	}
	if status == "resolved" {
		a.EndsAt = starts.Add(4 * time.Minute)
	}
	return a
}

func TestNotePath(t *testing.T) {
	got := NotePath(testAlert("firing"))
	want := "incidents/2026/07/1783435020-NodeDown-ab12cd34ef56.md"
	if got != want {
		t.Fatalf("NotePath = %q, want %q", got, want)
	}
}

func TestNotePathSanitizesLabels(t *testing.T) {
	a := testAlert("firing")
	a.Labels["alertname"] = "weird alert/name"
	got := NotePath(a)
	if !strings.Contains(got, "weird_alert_name") || strings.Contains(got, " ") {
		t.Fatalf("NotePath not sanitized: %q", got)
	}
}

func TestRenderNoteFiring(t *testing.T) {
	content := RenderNote(testAlert("firing"), []string{"incidents"})

	for _, want := range []string{
		"title: \"NodeDown - 2026-07-07 14:37\"",
		"free: true",
		"alertname: \"NodeDown\"",
		"severity: \"critical\"",
		"starts_at: \"2026-07-07T14:37:00Z\"",
		"fingerprint: \"ab12cd34ef56\"",
		"created_at: \"2026-07-07T14:37:00Z\"",
		"telegram_publish_at: \"2026-07-07T14:37:00Z\"",
		"telegram_publish_tags:\n  - \"incidents\"",
		"  instance: \"node-02:9100\"",
		// visible status callout
		"> 🔴 **FIRING** · 2026-07-07 14:37 UTC",
		"> **NodeDown** · critical · Node exporter target is down.",
		// description in body
		"Prometheus cannot scrape node-02.",
		"Postmortem: [[incidents/2026/07/ab12cd34ef56-postmortem]]",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("firing note missing %q\n---\n%s", want, content)
		}
	}
	// Must not carry the old inline status text.
	if strings.Contains(content, "**NodeDown** firing") {
		t.Error("old inline status text must not be present")
	}
	if strings.Contains(content, "-postmortem.md") {
		t.Error("postmortem wikilink must not carry the .md suffix")
	}
	// The resolution patch must find its target exactly once.
	if strings.Count(content, FiringPatchBlock(testAlert("firing").StartsAt)) != 1 {
		t.Errorf("firing patch block must occur exactly once\n---\n%s", content)
	}
}

func TestRenderNoteResolved(t *testing.T) {
	content := RenderNote(testAlert("resolved"), nil)

	for _, want := range []string{
		"status: resolved\nends_at: \"2026-07-07T14:41:00Z\"",
		// visible resolved callout
		"> ✅ **RESOLVED** · 2026-07-07 14:41 UTC",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("resolved note missing %q\n---\n%s", want, content)
		}
	}
	if strings.Contains(content, "🔴") {
		t.Error("firing emoji must not appear in resolved note")
	}
	if strings.Contains(content, "telegram_publish_at") {
		t.Error("telegram fields must be absent when no tags are configured")
	}
}

func TestRenderNoteSinkNeverWritesPostmortemPath(t *testing.T) {
	// Confirm PostmortemLink path is never a path the sink would upsert.
	// The sink only ever upserts NotePath(a), never PostmortemLink(a)+".md".
	a := testAlert("firing")
	incidentPath := NotePath(a)
	postmortemPath := PostmortemLink(a) + ".md"
	if incidentPath == postmortemPath {
		t.Fatal("incident path and postmortem path must differ")
	}
	if strings.HasSuffix(incidentPath, "-postmortem.md") {
		t.Errorf("NotePath must never produce a postmortem path: %q", incidentPath)
	}
	// The rendered note body contains the wikilink but not a path the sink writes.
	content := RenderNote(a, nil)
	if strings.Contains(content, postmortemPath) {
		t.Errorf("rendered note body must not contain the postmortem file path %q", postmortemPath)
	}
}

func TestResolutionPatchFlipsCalloutAndFrontmatter(t *testing.T) {
	a := testAlert("firing")
	firing := RenderNote(a, []string{"incidents"})
	endsAt := time.Date(2026, 7, 7, 14, 41, 0, 0, time.UTC)

	patched := strings.Replace(firing, FiringPatchBlock(a.StartsAt), ResolvedPatchBlock(a.StartsAt, endsAt), 1)

	// Frontmatter must flip.
	if !strings.Contains(patched, "status: resolved\nends_at: \"2026-07-07T14:41:00Z\"") {
		t.Fatalf("patch did not flip frontmatter status:\n%s", patched)
	}
	// Visible callout must flip.
	if !strings.Contains(patched, "> ✅ **RESOLVED** · 2026-07-07 14:41 UTC") {
		t.Fatalf("patch did not flip visible callout:\n%s", patched)
	}
	// Firing callout must be gone.
	if strings.Contains(patched, "🔴") {
		t.Fatal("firing emoji still present after patch")
	}
	// Postmortem link must survive untouched.
	if !strings.Contains(patched, "Postmortem: [[incidents/2026/07/ab12cd34ef56-postmortem]]") {
		t.Fatal("patch clobbered the postmortem link")
	}
}

func TestRenderIndexNote(t *testing.T) {
	content := RenderIndexNote()
	for _, want := range []string{
		"content:\n  - magazine",
		"magazine_include_files: \"incidents/**/*.md\"",
		"magazine_exclude_files: \"{incidents/**/index.md,incidents/**/*-postmortem.md}\"",
		"magazine_sort_property: starts_at",
		"free: true",
		"created_at:",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("index note missing %q\n---\n%s", want, content)
		}
	}
}
