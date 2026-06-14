package echo

import (
	"strings"
	"testing"
)

// b830Raw is derived from the actual failure captured in
// ~/.raqim/memory/projects/raqim/log/2026-06-13-s-20260613-b830-UNDISTILLED.md.
// The model emitted three JSON objects (two fenced, one inline) plus prose and
// self-correction commentary. The old extractor sliced first-{ to last-} and
// then hit the closing ``` after the last object, producing:
// "invalid character '`' after top-level value".
const b830Raw = "```json\n" +
	"{\"summary\":\"Session reviewed codebase architecture.\",\"entries\":[]}" + "\n```\n\n" +
	"This was a straightforward session with no durable findings.\n\n" +
	"{\"summary\":\"Session reviewed codebase architecture.\",\"entries\":[]}\n```\n\n" +
	"<hr />\n\nWait—let me re-read the actual session transcript.\n\n---\n\n" +
	"The user requested a file listing. Read-only session.\n\n" +
	"```json\n" +
	"{\"summary\":\"User catalogued all Go source files via a find call. Read-only session.\",\"entries\":[]}" + "\n```"

func TestValidateOutput(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		// existing cases — no regression
		{"valid", `{"summary":"did a thing","entries":[{"type":"gotcha","title":"t","body":"b","anchors":[],"tags":[],"expires":null}]}`, false},
		{"valid zero entries", `{"summary":"routine session","entries":[]}`, false},
		{"fenced json", "```json\n{\"summary\":\"s\",\"entries\":[]}\n```", false},
		{"prose around json", "here you go:\n{\"summary\":\"s\",\"entries\":[]}\ndone!", false},
		{"not json", `nope`, true},
		{"missing summary", `{"entries":[]}`, true},
		{"bad type", `{"summary":"s","entries":[{"type":"wisdom","title":"t","body":"b"}]}`, true},
		{"empty title", `{"summary":"s","entries":[{"type":"gotcha","title":"","body":"b"}]}`, true},
		{"title over 80", `{"summary":"s","entries":[{"type":"gotcha","title":"` + strings.Repeat("x", 81) + `","body":"b"}]}`, true},
		{"first para over 120 words", `{"summary":"s","entries":[{"type":"gotcha","title":"t","body":"` + strings.Repeat("word ", 121) + `"}]}`, true},
		{"long second para ok", `{"summary":"s","entries":[{"type":"gotcha","title":"t","body":"short first.\n\n` + strings.Repeat("word ", 200) + `"}]}`, false},

		// new extractor cases
		{
			name:    "fenced json with trailing prose",
			raw:     "```json\n{\"summary\":\"s\",\"entries\":[]}\n```\n\nsome trailing explanation",
			wantErr: false,
		},
		{
			name: "three objects only last valid — picks last",
			// first two unmarshal as rawOutput but have empty summary (wrong shape);
			// third has required summary field.
			raw:     `{"wrong":"shape"} {"also_wrong":"value"} {"summary":"correct","entries":[]}`,
			wantErr: false,
		},
		{
			name:    "last object malformed picks last valid",
			raw:     `{"summary":"valid","entries":[]} {"bad":}`,
			wantErr: false,
		},
		{
			name:    "pure garbage falls to dead-letter without panic",
			raw:     "no json here at all !!!",
			wantErr: true,
		},
		{
			name:    "b830 real failure fixture",
			raw:     b830Raw,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, errs := ValidateOutput(tt.raw)
			if (len(errs) > 0) != tt.wantErr {
				t.Errorf("errs=%v, wantErr=%v", errs, tt.wantErr)
			}
		})
	}
}

func TestTopLevelObjects(t *testing.T) {
	tests := []struct {
		name  string
		input string
		count int
	}{
		{"none", "no braces here", 0},
		{"one", `{"a":1}`, 1},
		{"two separated by prose", `{"a":1} some text {"b":2}`, 2},
		{"nested object counts as one", `{"a":{"b":1}}`, 1},
		{"brace in string not counted", `{"a":"}"}`, 1},
		{"escaped quote in string", `{"a":"\"}"} {"b":2}`, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := topLevelObjects(tt.input)
			if len(got) != tt.count {
				t.Errorf("got %d objects %v, want %d", len(got), got, tt.count)
			}
		})
	}
}
