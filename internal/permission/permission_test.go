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
