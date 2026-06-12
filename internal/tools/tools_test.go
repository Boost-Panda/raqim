package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadWriteEditBash(t *testing.T) {
	dir := t.TempDir()
	c := &Ctx{Cwd: dir, Touched: map[string]bool{}}

	out, ok := Execute(c, "write", json.RawMessage(`{"path":"a.txt","content":"hello world"}`))
	if !ok {
		t.Fatal(out)
	}
	out, ok = Execute(c, "read", json.RawMessage(`{"path":"a.txt"}`))
	if !ok || out != "hello world" {
		t.Fatalf("read: %q %v", out, ok)
	}
	out, ok = Execute(c, "edit", json.RawMessage(`{"path":"a.txt","old":"world","new":"raqim"}`))
	if !ok {
		t.Fatal(out)
	}
	if _, ok = Execute(c, "edit", json.RawMessage(`{"path":"a.txt","old":"nope","new":"x"}`)); ok {
		t.Error("edit of missing string should fail")
	}
	out, ok = Execute(c, "bash", json.RawMessage(`{"command":"cat a.txt"}`))
	if !ok || strings.TrimSpace(out) != "hello raqim" {
		t.Fatalf("bash: %q %v", out, ok)
	}
	if _, ok = Execute(c, "bash", json.RawMessage(`{"command":"exit 3"}`)); ok {
		t.Error("nonzero exit should be ok=false")
	}
	if len(c.Touched) != 1 {
		t.Errorf("touched: %v", c.Touched)
	}
}

func TestMemorySearch(t *testing.T) {
	mem := t.TempDir()
	entry := `---
id: 01J8FZ3K9QW2ABCDEF
type: gotcha
title: twilio webhook retries are not idempotent
---
dedup must key on MessageSid + status, not delivery.

## evidence
- session s-1
`
	write := func(rel, content string) {
		p := filepath.Join(mem, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	write("projects/saleskick/entries/20260612-twilio.md", entry)
	write("projects/saleskick/archive/20260101-old.md", "---\ntitle: twilio old\n---\ntwilio twilio twilio")
	write("projects/other/entries/20260612-x.md", "---\ntitle: twilio elsewhere\n---\ntwilio")
	write("global/entries/20260612-g.md", "---\nid: 01GLOBALAAAA\ntitle: global twilio note\n---\nglobal fact about twilio")

	out, ok := MemorySearch(mem, "saleskick", "twilio dedup")
	if !ok {
		t.Fatal(out)
	}
	if !strings.Contains(out, "{01J8FZ3}") || !strings.Contains(out, "dedup must key") {
		t.Errorf("missing project hit:\n%s", out)
	}
	if !strings.Contains(out, "global twilio note") {
		t.Errorf("missing global hit:\n%s", out)
	}
	if strings.Contains(out, "archive") || strings.Contains(out, "elsewhere") {
		t.Errorf("scope leak (archive or other project):\n%s", out)
	}
}
