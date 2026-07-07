package alertsink

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// IndexPath is where the magazine index note lives on the alerts instance.
const IndexPath = "incidents/index.md"

// NotePath builds the incident note path:
// incidents/YYYY/MM/<startsAt-unix>-<alertname>-<fingerprint>.md
// Year and month come from startsAt (UTC). The path is a pure function of the
// alert identity, so retries and re-deliveries always hit the same note.
func NotePath(a Alert) string {
	t := a.StartsAt.UTC()
	return fmt.Sprintf("incidents/%04d/%02d/%d-%s-%s.md",
		t.Year(), int(t.Month()), t.Unix(), sanitizePathPart(a.Name()), sanitizePathPart(a.Fingerprint))
}

// PostmortemLink is the wikilink target for the (agent-authored) postmortem
// note. The sink only ever writes the link, never the note at that path.
func PostmortemLink(a Alert) string {
	t := a.StartsAt.UTC()
	return fmt.Sprintf("incidents/%04d/%02d/%s-postmortem",
		t.Year(), int(t.Month()), sanitizePathPart(a.Fingerprint))
}

// sanitizePathPart keeps path segments to a safe character set.
func sanitizePathPart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	return out
}

// statusBlock renders the two adjacent frontmatter lines that change on
// resolution. Keeping them adjacent lets the firing -> resolved transition be
// a single find/replace patch, which never touches anything else in the note
// (an agent's postmortem link or edits survive).
func statusBlock(status string, endsAt time.Time) string {
	ends := "null"
	if !endsAt.IsZero() {
		ends = strconv.Quote(endsAt.UTC().Format(time.RFC3339))
	}
	return "status: " + status + "\nends_at: " + ends
}

// FiringStatusBlock is the exact substring the resolution patch searches for.
func FiringStatusBlock() string {
	return statusBlock("firing", time.Time{})
}

// ResolvedStatusBlock is the replacement substring for the resolution patch.
func ResolvedStatusBlock(endsAt time.Time) string {
	return statusBlock("resolved", endsAt)
}

// RenderNote renders the full incident note content for the given alert
// status. Content is deterministic for a given alert event, so re-deliveries
// upsert identical bytes.
//
// telegramTags, when non-empty, adds telegram_publish_at and
// telegram_publish_tags to the frontmatter, which makes trip2g publish the
// note as a Telegram post (and edit it in place when the note changes).
func RenderNote(a Alert, telegramTags []string) string {
	name := a.Name()
	starts := a.StartsAt.UTC().Format(time.RFC3339)
	status := "firing"
	endsAt := time.Time{}
	if a.Resolved() {
		status = "resolved"
		endsAt = a.EndsAt
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("title: " + yamlQuote(name+" - "+a.StartsAt.UTC().Format("2006-01-02 15:04")) + "\n")
	// trip2g paywalls notes by default; free: true makes the incident public.
	b.WriteString("free: true\n")
	b.WriteString("alertname: " + yamlQuote(name) + "\n")
	b.WriteString("severity: " + yamlQuote(a.Severity()) + "\n")
	b.WriteString(statusBlock(status, endsAt) + "\n")
	b.WriteString("starts_at: " + yamlQuote(starts) + "\n")
	b.WriteString("fingerprint: " + yamlQuote(a.Fingerprint) + "\n")
	if labels := sortedKeys(a.Labels); len(labels) > 0 {
		b.WriteString("labels:\n")
		for _, k := range labels {
			b.WriteString("  " + yamlKey(k) + ": " + yamlQuote(a.Labels[k]) + "\n")
		}
	}
	b.WriteString("created_at: " + yamlQuote(starts) + "\n")
	if len(telegramTags) > 0 {
		b.WriteString("telegram_publish_at: " + yamlQuote(starts) + "\n")
		b.WriteString("telegram_publish_tags:\n")
		for _, tag := range telegramTags {
			b.WriteString("  - " + yamlQuote(tag) + "\n")
		}
	}
	b.WriteString("---\n")

	// Body: front-load the human signal; trip2g truncates Telegram posts.
	b.WriteString("**" + name + "** " + status + " (severity: " + a.Severity() + ").\n")
	if s := a.Annotations["summary"]; s != "" {
		b.WriteString("\n" + s + "\n")
	}
	if d := a.Annotations["description"]; d != "" {
		b.WriteString("\n" + d + "\n")
	}
	if pairs := labelPairs(a.Labels); pairs != "" {
		b.WriteString("\nlabels: " + pairs + "\n")
	}
	b.WriteString("\nPostmortem: [[" + PostmortemLink(a) + "]]\n")
	return b.String()
}

// RenderIndexNote renders the magazine index note that aggregates all
// incident notes (newest first), excluding the index itself and postmortems.
func RenderIndexNote() string {
	// The exclude glob uses doublestar brace alternation: a bare
	// comma-separated pair is a single (never-matching) pattern.
	return `---
title: Incident log
free: true
content:
  - magazine
magazine_include_files: "incidents/**/*.md"
magazine_exclude_files: "{incidents/**/index.md,incidents/**/*-postmortem.md}"
magazine_sort_property: starts_at
created_at: "2026-01-01T00:00:00Z"
---
All incidents, newest first. Incident notes are written by alert-sink from
Alertmanager webhooks; postmortems are authored separately and linked from
each incident.
`
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func labelPairs(m map[string]string) string {
	var parts []string
	for _, k := range sortedKeys(m) {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ", ")
}

// yamlQuote renders a YAML double-quoted scalar.
func yamlQuote(s string) string {
	return strconv.Quote(s)
}

// yamlKey keeps frontmatter map keys to a plain identifier set.
func yamlKey(s string) string {
	return sanitizePathPart(s)
}
