package echo

import (
	"strings"
	"testing"
)

func TestValidateOutput(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
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
