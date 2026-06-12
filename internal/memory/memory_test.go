package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Table-driven entry frontmatter validation per CLAUDE.md hard rules.
func TestEntryValidation(t *testing.T) {
	valid := Entry{
		ID: "01J8FZ3K9QW2ABCDEF", Type: "gotcha", Title: "twilio retries are not idempotent",
		Status: "active", Created: "2026-06-12", Author: "echo",
	}
	tests := []struct {
		name   string
		mutate func(*Entry)
		wantOK bool
	}{
		{"valid", func(e *Entry) {}, true},
		{"valid human author", func(e *Entry) { e.Author = "human" }, true},
		{"valid needs-verify", func(e *Entry) { e.Status = "needs-verify" }, true},
		{"missing id", func(e *Entry) { e.ID = "" }, false},
		{"bad type", func(e *Entry) { e.Type = "wisdom" }, false},
		{"missing title", func(e *Entry) { e.Title = "" }, false},
		{"title over 80", func(e *Entry) { e.Title = strings.Repeat("x", 81) }, false},
		{"title exactly 80", func(e *Entry) { e.Title = strings.Repeat("x", 80) }, true},
		{"bad status", func(e *Entry) { e.Status = "zombie" }, false},
		{"missing created", func(e *Entry) { e.Created = "" }, false},
		{"bad author", func(e *Entry) { e.Author = "claude" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := valid
			tt.mutate(&e)
			errs := e.Validate()
			if (len(errs) == 0) != tt.wantOK {
				t.Errorf("Validate() = %v, wantOK=%v", errs, tt.wantOK)
			}
		})
	}
}

func TestRenderParseRoundTrip(t *testing.T) {
	e := &Entry{
		ID: "01J8FZ3K9QW2ABCDEF", Type: "gotcha", Title: "twilio webhook retries are not idempotent",
		Status: "active", Created: "2026-06-12", Updated: "2026-06-12", Author: "echo",
		Source:  []string{"s-20260612-a4f2"},
		Anchors: []Anchor{{Repo: "saleskick", Path: "services/lns/src/webhook.ts", SHA: "a1b2c3d"}},
		Tags:    []string{"whatsapp", "webhooks"},
		Expires: "2026-09-01",
		Body:    "dedup must key on MessageSid + status.\n\n## evidence\n- session s-20260612-a4f2",
	}
	got, err := Parse(e.Render())
	if err != nil {
		t.Fatal(err)
	}
	got.FilePath = ""
	if got.Render() != e.Render() {
		t.Errorf("round-trip mismatch:\n--- want\n%s\n--- got\n%s", e.Render(), got.Render())
	}
	if got.Anchors[0].SHA != "a1b2c3d" || got.Tags[1] != "webhooks" {
		t.Errorf("fields lost: %+v", got)
	}
}

func seedProject(t *testing.T) (memPath, project string) {
	t.Helper()
	memPath, project = t.TempDir(), "saleskick"
	entries := []*Entry{
		{ID: "01J8ABCXXXXXXXXXXX", Type: "decision", Title: "jsonb columns require schema contracts", Status: "active", Created: "2026-05-30", Author: "echo"},
		{ID: "01J7QRSXXXXXXXXXXX", Type: "decision", Title: "zernio over twilio for whatsapp templates", Status: "active", Created: "2026-04-12", Author: "human"},
		{ID: "01J8FZ3XXXXXXXXXXX", Type: "gotcha", Title: "twilio webhook retries are not idempotent", Status: "active", Created: "2026-06-12", Author: "echo"},
		{ID: "01J8DEFXXXXXXXXXXX", Type: "task", Title: "migrate lns cron jobs", Status: "active", Created: "2026-06-01", Author: "echo", Expires: "2026-07-01"},
		{ID: "01J8OLDXXXXXXXXXXX", Type: "gotcha", Title: "superseded thing", Status: "superseded", Created: "2026-01-01", Author: "echo"},
	}
	for _, e := range entries {
		e.Body = "body of " + e.Title
		if _, err := WriteEntry(memPath, project, e); err != nil {
			t.Fatal(err)
		}
	}
	docs := filepath.Join(ProjectDir(memPath, project), "docs")
	os.MkdirAll(docs, 0o755)
	os.WriteFile(filepath.Join(docs, "architecture.md"), []byte("# LNS and whatsapp pipeline\n\nstuff"), 0o644)
	os.WriteFile(filepath.Join(ProjectDir(memPath, project), "questions.md"),
		[]byte("# questions\n\n- oldest question\n- newest question\n"), 0o644)
	return
}

// MEMORY.md generation determinism per CLAUDE.md hard rules.
func TestIndexDeterminism(t *testing.T) {
	memPath, project := seedProject(t)
	first, err := GenIndex(memPath, project, 1500)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := GenIndex(memPath, project, 1500)
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatalf("run %d differs:\n--- first\n%s\n--- again\n%s", i, first, again)
		}
	}

	tests := []struct {
		name string
		want string
	}{
		{"header counts", "> 4 active · 2 open questions"},
		{"fixed order", "## documents"},
		{"decision newest first", "- jsonb columns require schema contracts {01J8ABC} (05-30)\n- zernio over twilio for whatsapp templates {01J7QRS} (04-12)"},
		{"gotcha line", "- twilio webhook retries are not idempotent {01J8FZ3} (06-12)"},
		{"task due date", "- migrate lns cron jobs {01J8DEF} (due 07-01)"},
		{"doc line", "- docs/architecture.md — LNS and whatsapp pipeline"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(first, tt.want) {
				t.Errorf("index missing %q:\n%s", tt.want, first)
			}
		})
	}
	if strings.Contains(first, "superseded thing") {
		t.Error("superseded entry leaked into index")
	}
	if !strings.Contains(first, "## decisions\n") || strings.Index(first, "## decisions") > strings.Index(first, "## gotchas") {
		t.Error("section order broken")
	}
}

func TestIndexBudgetTruncation(t *testing.T) {
	memPath, project := t.TempDir(), "big"
	for i := 0; i < 60; i++ {
		e := &Entry{ID: strings.Repeat("0", 10) + string(rune('A'+i%26)) + strings.Repeat("X", 7),
			Type: "reference", Title: strings.Repeat("very long reference title ", 3) + string(rune('a'+i%26)),
			Status: "active", Created: "2026-06-01", Author: "echo", Body: "b"}
		e.ID = string(rune('A'+i/26)) + string(rune('A'+i%26)) + strings.Repeat("0", 16)
		if _, err := WriteEntry(memPath, project, e); err != nil {
			t.Fatal(err)
		}
	}
	out, err := GenIndex(memPath, project, 300)
	if err != nil {
		t.Fatal(err)
	}
	if tokens(out) > 300 {
		t.Errorf("over budget: %d tokens", tokens(out))
	}
	if !strings.Contains(out, "more, use memory_search") {
		t.Errorf("missing truncation marker:\n%s", out)
	}
}

func TestQuarantine(t *testing.T) {
	memPath, project := t.TempDir(), "p"
	dir := filepath.Join(ProjectDir(memPath, project), "entries")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "20260612-bad.md"), []byte("---\ntype: nonsense\n---\nbody"), 0o644)
	entries, quarantined, err := LoadEntries(memPath, project)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 || len(quarantined) != 1 {
		t.Fatalf("entries=%d quarantined=%v", len(entries), quarantined)
	}
	if _, err := os.Stat(filepath.Join(ProjectDir(memPath, project), "archive", "invalid", "20260612-bad.md")); err != nil {
		t.Error("invalid entry not moved to archive/invalid/")
	}
}

func TestInject(t *testing.T) {
	memPath, project := seedProject(t)
	os.MkdirAll(filepath.Join(memPath, "global", "docs"), 0o755)
	os.WriteFile(filepath.Join(memPath, "global", "docs", "profile.md"), []byte("# profile\nprefers go"), 0o644)
	if _, err := WriteIndex(memPath, project, 1500); err != nil {
		t.Fatal(err)
	}
	out := Inject(memPath, project, 3000)
	for _, want := range []string{"prefers go", "memory index", "newest question"} {
		if !strings.Contains(out, want) {
			t.Errorf("injection missing %q", want)
		}
	}
	qi := strings.Index(out, "newest question")
	if oi := strings.Index(out, "oldest question"); oi >= 0 && oi < qi {
		t.Error("questions not newest-first")
	}

	empty := Inject(memPath, "fresh-project", 3000)
	if !strings.Contains(empty, "memory is empty") {
		t.Errorf("bootstrap block missing: %s", empty)
	}
}
