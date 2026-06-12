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
	Feedback string // user's one-line reason on [n]; "" means generic "denied by user"
	Exit     bool   // user typed /exit (or stdin closed): end the session
}

type Answer int

const (
	AnswerYes Answer = iota
	AnswerNo
	AnswerAll
	AnswerExit
	AnswerInvalid
)

// ParseAnswer interprets one line of permission-prompt input:
// y/n/a case-insensitive, whitespace-trimmed, plus /exit. Anything else
// is invalid and must re-prompt — never mapped to deny.
func ParseAnswer(line string) Answer {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y":
		return AnswerYes
	case "n":
		return AnswerNo
	case "a":
		return AnswerAll
	case "/exit":
		return AnswerExit
	default:
		return AnswerInvalid
	}
}

// SanitizeFeedback validates the why-prompt answer before it reaches the
// model. Empty, whitespace-only, or command-like ("/...") input returns
// "" — the caller substitutes the generic "denied by user".
func SanitizeFeedback(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "/") {
		return ""
	}
	return s
}

type Engine struct {
	grants   map[string]bool
	in       *bufio.Reader
	out      io.Writer
	cwd      string
	raqimDir string
}

// New takes an already-buffered reader so the caller's readline loop and
// the permission prompts share one buffer — two bufio.Readers over the
// same fd starve each other on piped input.
func New(in *bufio.Reader, out io.Writer, cwd, raqimDir string) *Engine {
	return &Engine{grants: map[string]bool{}, in: in, out: out, cwd: cwd, raqimDir: raqimDir}
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
	exit := Decision{Feedback: "session ending", Exit: true}
	fmt.Fprintf(e.out, "\n%s: %s\n", tool, summary)
	for {
		fmt.Fprintf(e.out, "[y] run once  [n] deny + tell raqim why  [a] allow all %s this session\n> ", class)
		line, err := e.in.ReadString('\n')
		if err != nil && strings.TrimSpace(line) == "" {
			return exit // stdin closed: same semantics as /exit
		}
		switch ParseAnswer(line) {
		case AnswerYes:
			return Decision{Allowed: true}
		case AnswerAll:
			e.grants[class] = true
			return Decision{Allowed: true}
		case AnswerExit:
			return exit
		case AnswerNo:
			fmt.Fprint(e.out, "why? > ")
			why, _ := e.in.ReadString('\n')
			if ParseAnswer(why) == AnswerExit {
				return exit
			}
			return Decision{Allowed: false, Feedback: SanitizeFeedback(why)}
		default: // unrecognized: re-prompt, never deny
		}
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
