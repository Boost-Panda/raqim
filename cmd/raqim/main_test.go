package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadInput(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      string
		wantPaste bool
	}{
		{
			name:      "single line normal",
			input:     "hello world\n",
			want:      "hello world",
			wantPaste: false,
		},
		{
			name:      "single line whitespace stripped",
			input:     "  hello  \n",
			want:      "hello",
			wantPaste: false,
		},
		{
			name:      "empty line",
			input:     "\n",
			want:      "",
			wantPaste: false,
		},
		{
			name:      "bracketed paste single line",
			input:     "\x1b[200~hello world\x1b[201~\n",
			want:      "hello world",
			wantPaste: true,
		},
		{
			name:      "bracketed paste multi-line coalesced",
			input:     "\x1b[200~line one\nline two\nline three\x1b[201~\n",
			want:      "line one\nline two\nline three",
			wantPaste: true,
		},
		{
			name:      "bracketed paste trims trailing newline",
			input:     "\x1b[200~hello\n\x1b[201~\n",
			want:      "hello",
			wantPaste: true,
		},
		{
			name:      "bracketed paste preserves internal spaces",
			input:     "\x1b[200~  indented line\n  another\x1b[201~\n",
			want:      "  indented line\n  another",
			wantPaste: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(tt.input))
			text, pasted, err := readInput(r)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if text != tt.want {
				t.Errorf("text = %q, want %q", text, tt.want)
			}
			if pasted != tt.wantPaste {
				t.Errorf("pasted = %v, want %v", pasted, tt.wantPaste)
			}
		})
	}
}
