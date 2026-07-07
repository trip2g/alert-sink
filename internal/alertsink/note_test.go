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
		"status: firing\nends_at: null",
		"starts_at: \"2026-07-07T14:37:00Z\"",
		"fingerprint: \"ab12cd34ef56\"",
		"created_at: \"2026-07-07T14:37:00Z\"",
		"telegram_publish_at: \"2026-07-07T14:37:00Z\"",
		"telegram_publish_tags:\n  - \"incidents\"",
		"  instance: \"node-02:9100\"",
		"**NodeDown** firing (severity: critical).",
		"Node exporter target is down.",
		"Postmortem: [[incidents/2026/07/ab12cd34ef56-postmortem]]",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("firing note missing %q\n---\n%s", want, content)
		}
	}
	if strings.Contains(content, "-postmortem.md") {
		t.Error("postmortem wikilink must not carry the .md suffix")
	}
	// The resolution patch must find its target exactly once.
	if strings.Count(content, FiringStatusBlock()) != 1 {
		t.Errorf("firing status block must occur exactly once\n---\n%s", content)
	}
}

func TestRenderNoteResolved(t *testing.T) {
	content := RenderNote(testAlert("resolved"), nil)

	for _, want := range []string{
		"status: resolved\nends_at: \"2026-07-07T14:41:00Z\"",
		"**NodeDown** resolved (severity: critical).",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("resolved note missing %q\n---\n%s", want, content)
		}
	}
	if strings.Contains(content, "telegram_publish_at") {
		t.Error("telegram fields must be absent when no tags are configured")
	}
}

func TestResolutionPatchTransformsFiringNote(t *testing.T) {
	firing := RenderNote(testAlert("firing"), []string{"incidents"})
	endsAt := time.Date(2026, 7, 7, 14, 41, 0, 0, time.UTC)

	// Simulate an agent edit: appended postmortem link must survive.
	edited := firing + "\nSee [[incidents/2026/07/ab12cd34ef56-postmortem]] for analysis.\n"

	patched := strings.Replace(edited, FiringStatusBlock(), ResolvedStatusBlock(endsAt), 1)
	if !strings.Contains(patched, "status: resolved\nends_at: \"2026-07-07T14:41:00Z\"") {
		t.Fatalf("patch did not flip status:\n%s", patched)
	}
	if !strings.Contains(patched, "for analysis") {
		t.Fatal("patch clobbered the agent's edit")
	}
	if strings.Contains(patched, "status: firing") {
		t.Fatal("firing status still present after patch")
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
