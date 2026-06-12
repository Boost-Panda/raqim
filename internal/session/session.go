// Package session implements jsonl transcripts (plan §3.3) and the ttl
// reaper (§3.11).
package session

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const OutputCap = 16 * 1024 // chars; tool outputs beyond this are tail-truncated

type Event struct {
	T         string          `json:"t"`
	Session   string          `json:"session,omitempty"`
	Project   string          `json:"project,omitempty"`
	Model     string          `json:"model,omitempty"`
	Gen       *int            `json:"gen,omitempty"`
	Started   string          `json:"started,omitempty"`
	TS        string          `json:"ts,omitempty"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	OK        *bool           `json:"ok,omitempty"`
	Output    string          `json:"output,omitempty"`
	Truncated *bool           `json:"truncated,omitempty"`
	In        int64           `json:"in,omitempty"`
	Out       int64           `json:"out,omitempty"`
	CacheRead int64           `json:"cache_read,omitempty"`
	CtxPct    int             `json:"ctx_pct,omitempty"`
	Reason    string          `json:"reason,omitempty"`
}

func boolp(b bool) *bool { return &b }
func now() string        { return time.Now().Format(time.RFC3339) }

type Session struct {
	ID      string
	Project string
	Path    string
	f       *os.File
	ended   bool
}

func Dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".raqim", "sessions")
}

// New creates the jsonl file and writes the meta event.
func New(project, model string) (*Session, error) {
	b := make([]byte, 2)
	rand.Read(b)
	id := fmt.Sprintf("s-%s-%s", time.Now().Format("20060102"), hex.EncodeToString(b))
	dir := filepath.Join(Dir(), project)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s := &Session{ID: id, Project: project, Path: path, f: f}
	gen := 0
	return s, s.Append(Event{T: "meta", Session: id, Project: project, Model: model, Gen: &gen, Started: now()})
}

func (s *Session) Append(ev Event) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	if _, err := s.f.Write(append(line, '\n')); err != nil {
		return err
	}
	return s.f.Sync()
}

func (s *Session) User(text string)      { s.Append(Event{T: "user", TS: now(), Text: text}) }
func (s *Session) Assistant(text string) { s.Append(Event{T: "assistant", TS: now(), Text: text}) }

func (s *Session) ToolCall(id, name string, args json.RawMessage) {
	s.Append(Event{T: "tool_call", ID: id, Name: name, Args: args})
}

func (s *Session) ToolResult(id string, ok bool, output string) {
	trunc := false
	if len(output) > OutputCap {
		output, trunc = output[:OutputCap], true
	}
	s.Append(Event{T: "tool_result", ID: id, OK: boolp(ok), Output: output, Truncated: boolp(trunc)})
}

func (s *Session) Usage(in, out, cacheRead int64, ctxPct int) {
	s.Append(Event{T: "usage", In: in, Out: out, CacheRead: cacheRead, CtxPct: ctxPct})
}

// End writes the end event once and closes the file.
func (s *Session) End(reason string) {
	if s.ended {
		return
	}
	s.ended = true
	s.Append(Event{T: "end", Reason: reason, TS: now()})
	s.f.Close()
}

// Load reads all events from a transcript.
func Load(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var evs []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			return nil, fmt.Errorf("%s: bad line: %w", path, err)
		}
		evs = append(evs, ev)
	}
	return evs, sc.Err()
}

// Find locates a transcript by session id across all projects.
func Find(id string) (path, project string, err error) {
	matches, _ := filepath.Glob(filepath.Join(Dir(), "*", id+".jsonl"))
	if len(matches) == 0 {
		return "", "", fmt.Errorf("session %s not found under %s", id, Dir())
	}
	return matches[0], filepath.Base(filepath.Dir(matches[0])), nil
}

// All returns every transcript path keyed by project.
func All() map[string][]string {
	out := map[string][]string{}
	matches, _ := filepath.Glob(filepath.Join(Dir(), "*", "*.jsonl"))
	for _, m := range matches {
		p := filepath.Base(filepath.Dir(m))
		out[p] = append(out[p], m)
	}
	return out
}

// HasEnd reports whether the transcript has a clean end event.
func HasEnd(evs []Event) bool {
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].T == "end" {
			return true
		}
	}
	return false
}
