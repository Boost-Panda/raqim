package permission

import (
	"bufio"
	"strings"
	"testing"
)

// Table-driven denylist tests per CLAUDE.md hard rules.
func TestDenylist(t *testing.T) {
	cwd := "/Users/me/code/proj"
	raqim := "/Users/me/.raqim"
	tests := []struct {
		name string
		tool string
		arg  string
		deny bool
	}{
		{"plain bash", "bash", "go test ./...", false},
		{"sudo", "bash", "sudo rm /etc/hosts", true},
		{"sudo chained", "bash", "ls && sudo reboot", true},
		{"sudo substring ok", "bash", "echo sudoku", false},
		{"force push", "bash", "git push --force origin main", true},
		{"force with lease", "bash", "git push --force-with-lease", true},
		{"push -f", "bash", "git push -f", true},
		{"plain push", "bash", "git push origin main", false},
		{"rm -rf inside cwd", "bash", "rm -rf build", false},
		{"rm -rf dot", "bash", "rm -rf ./tmp/cache", false},
		{"rm -rf outside cwd", "bash", "rm -rf /tmp/other", true},
		{"rm -rf home", "bash", "rm -rf ~/stuff", true},
		{"rm -fr outside", "bash", "rm -fr /var/log", true},
		{"rm non-recursive outside", "bash", "rm /tmp/file.txt", false},
		{"bash touching raqim dir", "bash", "cat /Users/me/.raqim/config.toml", true},
		{"bash tilde raqim", "bash", "ls ~/.raqim/sessions", true},
		{"write normal", "write", "/Users/me/code/proj/main.go", false},
		{"write under raqim", "write", "/Users/me/.raqim/memory/hack.md", true},
		{"edit under raqim", "edit", "/Users/me/.raqim/config.toml", true},
		{"read never denied", "read", "/Users/me/.raqim/config.toml", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := Denied(tt.tool, tt.arg, cwd, raqim)
			if (reason != "") != tt.deny {
				t.Errorf("Denied(%q,%q) = %q, want deny=%v", tt.tool, tt.arg, reason, tt.deny)
			}
		})
	}
}

// Table-driven prompt input parsing: valid, invalid, /exit, whitespace,
// case variants.
func TestParseAnswer(t *testing.T) {
	tests := []struct {
		in   string
		want Answer
	}{
		{"y", AnswerYes},
		{"Y", AnswerYes},
		{"  y  \n", AnswerYes},
		{"n", AnswerNo},
		{" N\n", AnswerNo},
		{"a", AnswerAll},
		{"A ", AnswerAll},
		{"/exit", AnswerExit},
		{" /EXIT \n", AnswerExit},
		{"exit", AnswerInvalid},
		{"q", AnswerInvalid},
		{"yes please", AnswerInvalid},
		{"yes", AnswerInvalid},
		{"", AnswerInvalid},
		{"   \n", AnswerInvalid},
		{"/quit", AnswerInvalid},
		{"no", AnswerInvalid},
	}
	for _, tt := range tests {
		t.Run("input "+strings.TrimSpace(tt.in), func(t *testing.T) {
			if got := ParseAnswer(tt.in); got != tt.want {
				t.Errorf("ParseAnswer(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeFeedback(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"use the makefile", "use the makefile"},
		{"  trimmed reason \n", "trimmed reason"},
		{"", ""},
		{"   \n", ""},
		{"/q", ""},
		{"/help me", ""},
	}
	for _, tt := range tests {
		if got := SanitizeFeedback(tt.in); got != tt.want {
			t.Errorf("SanitizeFeedback(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPromptReprompts(t *testing.T) {
	cwd, raqim := "/tmp/proj", "/tmp/.raqim"
	// four invalid answers, then a valid y — must re-prompt, never deny
	out := &strings.Builder{}
	e := New(bufio.NewReader(strings.NewReader("exit\nq\nyes please\n\ny\n")), out, cwd, raqim)
	d := e.Check("bash", "ls")
	if !d.Allowed || d.Exit {
		t.Errorf("expected allow after re-prompts, got %+v", d)
	}
	if n := strings.Count(out.String(), "[y] run once"); n != 5 {
		t.Errorf("option line printed %d times, want 5 (1 + 4 re-prompts)", n)
	}
}

func TestPromptExit(t *testing.T) {
	cwd, raqim := "/tmp/proj", "/tmp/.raqim"
	tests := []struct {
		name  string
		input string
	}{
		{"/exit at prompt", "/exit\n"},
		{"/exit after invalid", "q\n/exit\n"},
		{"/exit at why-prompt", "n\n/exit\n"},
		{"stdin closed", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := New(bufio.NewReader(strings.NewReader(tt.input)), &strings.Builder{}, cwd, raqim)
			d := e.Check("bash", "ls")
			if !d.Exit || d.Allowed || d.Feedback != "session ending" {
				t.Errorf("expected exit decision, got %+v", d)
			}
		})
	}
}

func TestWhyPromptSanitized(t *testing.T) {
	cwd, raqim := "/tmp/proj", "/tmp/.raqim"
	tests := []struct {
		name, input, want string
	}{
		{"empty why", "n\n\n", ""},
		{"whitespace why", "n\n   \n", ""},
		{"command why", "n\n/q\n", ""},
		{"real reason", "n\nuse the makefile\n", "use the makefile"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := New(bufio.NewReader(strings.NewReader(tt.input)), &strings.Builder{}, cwd, raqim)
			d := e.Check("bash", "ls")
			if d.Allowed || d.Exit || d.Feedback != tt.want {
				t.Errorf("got %+v, want feedback %q", d, tt.want)
			}
		})
	}
}

func TestGrantsAndPrompt(t *testing.T) {
	cwd, raqim := "/tmp/proj", "/tmp/.raqim"

	// read/memory_search never prompt
	e := New(bufio.NewReader(strings.NewReader("")), &strings.Builder{}, cwd, raqim)
	if d := e.Check("read", "/x"); !d.Allowed {
		t.Error("read should auto-allow")
	}

	// [a] grants the class for the session, but denylist still applies
	out := &strings.Builder{}
	e = New(bufio.NewReader(strings.NewReader("a\n")), out, cwd, raqim)
	if d := e.Check("bash", "ls"); !d.Allowed {
		t.Error("[a] should allow")
	}
	if d := e.Check("bash", "pwd"); !d.Allowed {
		t.Error("grant should persist")
	}
	if d := e.Check("bash", "sudo ls"); d.Allowed {
		t.Error("denylist must override allow-all")
	}

	// [n] collects feedback
	e = New(bufio.NewReader(strings.NewReader("n\nuse the makefile\n")), &strings.Builder{}, cwd, raqim)
	d := e.Check("bash", "go build")
	if d.Allowed || d.Feedback != "use the makefile" {
		t.Errorf("deny flow: %+v", d)
	}

	// bash and mutate grants are separate classes
	e = New(bufio.NewReader(strings.NewReader("a\ny\n")), &strings.Builder{}, cwd, raqim)
	e.Check("bash", "ls")
	if d := e.Check("write", "/tmp/proj/f.go"); !d.Allowed {
		t.Error("mutate prompt should have been answered y")
	}
}
