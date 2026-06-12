package session

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Table-driven jsonl round-trip per CLAUDE.md hard rules.
func TestJSONLRoundTrip(t *testing.T) {
	gen := 0
	tests := []struct {
		name string
		ev   Event
	}{
		{"meta", Event{T: "meta", Session: "s-20260612-a4f2", Project: "raqim", Model: "claude-sonnet-4-6", Gen: &gen, Started: "2026-06-12T00:00:00Z"}},
		{"user", Event{T: "user", TS: "2026-06-12T00:00:01Z", Text: "hello"}},
		{"assistant", Event{T: "assistant", TS: "2026-06-12T00:00:02Z", Text: "hi"}},
		{"tool_call", Event{T: "tool_call", ID: "tc_1", Name: "bash", Args: json.RawMessage(`{"command":"go test ./..."}`)}},
		{"tool_result ok", Event{T: "tool_result", ID: "tc_1", OK: boolp(true), Output: "ok\n", Truncated: boolp(false)}},
		{"tool_result denied", Event{T: "tool_result", ID: "tc_2", OK: boolp(false), Output: "denied: sudo", Truncated: boolp(false)}},
		{"usage", Event{T: "usage", In: 1234, Out: 567, CacheRead: 8900, CtxPct: 41}},
		{"end", Event{T: "end", Reason: "exit", TS: "2026-06-12T00:01:00Z"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.ev)
			if err != nil {
				t.Fatal(err)
			}
			var got Event
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			b2, _ := json.Marshal(got)
			if string(b) != string(b2) {
				t.Errorf("round-trip mismatch:\n  first:  %s\n  second: %s", b, b2)
			}
		})
	}
}

func TestSessionFileLifecycle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, err := New("testproj", "claude-sonnet-4-6")
	if err != nil {
		t.Fatal(err)
	}
	s.User("plant a fact")
	s.ToolCall("tc_1", "bash", json.RawMessage(`{"command":"true"}`))
	s.ToolResult("tc_1", true, strings.Repeat("x", OutputCap+100))
	s.Usage(10, 20, 30, 5)
	s.Assistant("done")
	s.End("exit")
	s.End("exit") // idempotent

	evs, err := Load(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 7 {
		t.Fatalf("want 7 events, got %d", len(evs))
	}
	if !HasEnd(evs) {
		t.Error("end event missing")
	}
	var tr Event
	for _, e := range evs {
		if e.T == "tool_result" {
			tr = e
		}
	}
	if len(tr.Output) != OutputCap || tr.Truncated == nil || !*tr.Truncated {
		t.Errorf("output not capped: len=%d truncated=%v", len(tr.Output), tr.Truncated)
	}
	if _, err := os.Stat(s.Path); err != nil {
		t.Error(err)
	}
	if _, proj, err := Find(s.ID); err != nil || proj != "testproj" {
		t.Errorf("Find: %v %q", err, proj)
	}
}
