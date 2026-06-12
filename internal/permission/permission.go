// Package permission implements the prompt engine, session grants, and
// the always-on denylist (plan §3.4).
package permission

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

// Class returns the grant class for a tool: "" (auto-allowed), "bash",
// or "mutate". Grants approve per class, never per tool.
func Class(tool string) string {
	switch tool {
	case "bash":
		return "bash"
	case "write", "edit":
		return "mutate"
	default: // read, memory_search
		return ""
	}
}

type Decision struct {
	Allowed  bool
	Feedback string // user's one-line reason on [n]
}

type Engine struct {
	grants   map[string]bool
	in       *bufio.Reader
	out      io.Writer
	cwd      string
	raqimDir string
}

func New(in io.Reader, out io.Writer, cwd, raqimDir string) *Engine {
	return &Engine{grants: map[string]bool{}, in: bufio.NewReader(in), out: out, cwd: cwd, raqimDir: raqimDir}
}

// Check runs denylist then grants then the interactive prompt.
// summary is what's shown to the user (command or path).
func (e *Engine) Check(tool, summary string) Decision {
	if reason := Denied(tool, summary, e.cwd, e.raqimDir); reason != "" {
		fmt.Fprintf(e.out, "denied (%s): %s\n", reason, summary)
		return Decision{Allowed: false, Feedback: "denied by policy: " + reason}
	}
	class := Class(tool)
	if class == "" || e.grants[class] {
		return Decision{Allowed: true}
	}
	fmt.Fprintf(e.out, "\n%s: %s\n[y] run once  [n] deny + tell raqim why  [a] allow all %s this session\n> ", tool, summary, class)
	line, _ := e.in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y":
		return Decision{Allowed: true}
	case "a":
		e.grants[class] = true
		return Decision{Allowed: true}
	default:
		fmt.Fprint(e.out, "why? > ")
		why, _ := e.in.ReadString('\n')
		return Decision{Allowed: false, Feedback: strings.TrimSpace(why)}
	}
}

var (
	forcePush = regexp.MustCompile(`\bgit\b.*\bpush\b.*(--force\S*|\s-f\b)`)
	sudoRe    = regexp.MustCompile(`(^|[;&|]\s*)sudo\b`)
	rmRecRe   = regexp.MustCompile(`(^|[;&|]\s*)rm\s+(-\S*\s+)*-\S*[rR]`)
)

// Denied returns a non-empty reason when the call violates the denylist.
// It applies even under allow-all grants. arg is the bash command, or the
// target path for write/edit.
func Denied(tool, arg, cwd, raqimDir string) string {
	if tool == "write" || tool == "edit" {
		if underDir(absIn(arg, cwd), raqimDir) {
			return "path under ~/.raqim"
		}
		return ""
	}
	if tool != "bash" {
		return ""
	}
	cmd := arg
	if sudoRe.MatchString(cmd) {
		return "sudo"
	}
	if forcePush.MatchString(cmd) {
		return "git push --force"
	}
	if strings.Contains(cmd, raqimDir) || strings.Contains(cmd, "~/.raqim") {
		return "path under ~/.raqim"
	}
	if rmRecRe.MatchString(cmd) {
		for _, tok := range strings.Fields(cmd) {
			if strings.HasPrefix(tok, "-") || tok == "rm" {
				continue
			}
			if !underDir(absIn(expandHome(tok), cwd), cwd) {
				return "rm -rf outside cwd"
			}
		}
	}
	return ""
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		return "/HOME" + strings.TrimPrefix(p, "~") // sentinel: never under cwd
	}
	return p
}

func absIn(p, base string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}

func underDir(p, dir string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
