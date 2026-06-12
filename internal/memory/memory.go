// Package memory implements the markdown tree io: entry parse/validate,
// deterministic MEMORY.md generation, injection assembly, and the
// one-commit write protocol (memory-spec §2–§7).
package memory

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	Types    = map[string]bool{"decision": true, "gotcha": true, "insight": true, "reference": true, "task": true}
	Statuses = map[string]bool{"active": true, "needs-verify": true, "superseded": true}
	Authors  = map[string]bool{"echo": true, "dream": true, "human": true}
)

type Anchor struct{ Repo, Path, SHA string }

type Entry struct {
	ID, Type, Title, Status, Created, Updated, Author string
	Source                                            []string
	Anchors                                           []Anchor
	Supersedes, Expires                               string
	Tags                                              []string
	Body                                              string
	FilePath                                          string // set when loaded from disk
}

// Validate enforces memory-spec §2 required fields.
func (e *Entry) Validate() []string {
	var errs []string
	if e.ID == "" {
		errs = append(errs, "missing id")
	}
	if !Types[e.Type] {
		errs = append(errs, "bad type: "+e.Type)
	}
	if e.Title == "" {
		errs = append(errs, "missing title")
	} else if len(e.Title) > 80 {
		errs = append(errs, fmt.Sprintf("title %d chars, max 80", len(e.Title)))
	}
	if !Statuses[e.Status] {
		errs = append(errs, "bad status: "+e.Status)
	}
	if e.Created == "" {
		errs = append(errs, "missing created")
	}
	if !Authors[e.Author] {
		errs = append(errs, "bad author: "+e.Author)
	}
	return errs
}

func (e *Entry) ShortID() string {
	if len(e.ID) >= 7 {
		return e.ID[:7]
	}
	return e.ID
}

// FirstParaWords counts the words of the first body paragraph (≤120 rule).
func (e *Entry) FirstParaWords() int {
	for _, p := range strings.Split(strings.TrimSpace(e.Body), "\n\n") {
		if p = strings.TrimSpace(p); p != "" {
			return len(strings.Fields(p))
		}
	}
	return 0
}

// Render produces the canonical on-disk form, deterministic field order.
func (e *Entry) Render() string {
	var b strings.Builder
	w := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	b.WriteString("---\n")
	w("id", e.ID)
	w("type", e.Type)
	w("title", e.Title)
	w("status", e.Status)
	w("created", e.Created)
	w("updated", e.Updated)
	w("author", e.Author)
	if len(e.Source) > 0 {
		w("source", "["+strings.Join(e.Source, ", ")+"]")
	}
	if len(e.Anchors) > 0 {
		b.WriteString("anchors:\n")
		for _, a := range e.Anchors {
			fmt.Fprintf(&b, "  - repo: %s\n    path: %s\n    sha: %s\n", a.Repo, a.Path, a.SHA)
		}
	}
	w("supersedes", e.Supersedes)
	w("expires", e.Expires)
	if len(e.Tags) > 0 {
		w("tags", "["+strings.Join(e.Tags, ", ")+"]")
	}
	b.WriteString("---\n")
	b.WriteString(strings.TrimSpace(e.Body))
	b.WriteString("\n")
	return b.String()
}

// Parse reads the canonical entry form back. Hand-rolled for exactly the
// shape Render writes (no yaml dep allowed).
func Parse(content string) (*Entry, error) {
	if !strings.HasPrefix(content, "---\n") {
		return nil, fmt.Errorf("no frontmatter")
	}
	end := strings.Index(content[4:], "\n---")
	if end < 0 {
		return nil, fmt.Errorf("unterminated frontmatter")
	}
	fm, body := content[4:4+end], strings.TrimPrefix(content[4+end+4:], "\n")
	e := &Entry{Body: strings.TrimSpace(body)}
	lines := strings.Split(fm, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line == "anchors:" {
			for i+1 < len(lines) && strings.HasPrefix(lines[i+1], "  ") {
				i++
				l := strings.TrimSpace(lines[i])
				if v, ok := strings.CutPrefix(l, "- repo: "); ok {
					e.Anchors = append(e.Anchors, Anchor{Repo: v})
				} else if v, ok := strings.CutPrefix(l, "path: "); ok && len(e.Anchors) > 0 {
					e.Anchors[len(e.Anchors)-1].Path = v
				} else if v, ok := strings.CutPrefix(l, "sha: "); ok && len(e.Anchors) > 0 {
					e.Anchors[len(e.Anchors)-1].SHA = v
				}
			}
			continue
		}
		k, v, ok := strings.Cut(line, ": ")
		if !ok {
			continue
		}
		v = strings.TrimSpace(stripComment(v))
		switch k {
		case "id":
			e.ID = v
		case "type":
			e.Type = v
		case "title":
			e.Title = v
		case "status":
			e.Status = v
		case "created":
			e.Created = v
		case "updated":
			e.Updated = v
		case "author":
			e.Author = v
		case "supersedes":
			e.Supersedes = v
		case "expires":
			e.Expires = v
		case "source":
			e.Source = parseList(v)
		case "tags":
			e.Tags = parseList(v)
		}
	}
	return e, nil
}

func stripComment(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		return v[:i]
	}
	return v
}

func parseList(v string) []string {
	v = strings.Trim(strings.TrimSpace(v), "[]")
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// Slug derives the frozen kebab filename slug (≤6 words) from a title.
var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

func Slug(title string) string {
	words := strings.Fields(strings.ToLower(title))
	if len(words) > 6 {
		words = words[:6]
	}
	s := nonAlnum.ReplaceAllString(strings.Join(words, "-"), "-")
	return strings.Trim(s, "-")
}

// --- tree io ---

func ProjectDir(memPath, project string) string {
	return filepath.Join(memPath, "projects", project)
}

// InitRepo git-inits and seeds the memory repo on first run (plan §3.1).
func InitRepo(memPath string) error {
	if _, err := os.Stat(filepath.Join(memPath, ".git")); err == nil {
		return nil
	}
	for _, d := range []string{"global/docs", "global/entries", "projects"} {
		if err := os.MkdirAll(filepath.Join(memPath, d), 0o755); err != nil {
			return err
		}
	}
	profile := filepath.Join(memPath, "global", "docs", "profile.md")
	if _, err := os.Stat(profile); os.IsNotExist(err) {
		os.WriteFile(profile, []byte("# profile\n\nwho you are, conventions, preferences. edit me.\n"), 0o644)
	}
	os.WriteFile(filepath.Join(memPath, ".gitignore"), []byte(".index/\n"), 0o644)
	cfg := "[budgets]\nindex_tokens = 1500\ninject_tokens = 3000\nentry_first_para_words = 120\n\n[sessions]\nttl_days = 30\ndistill_before_delete = true\n"
	os.WriteFile(filepath.Join(memPath, "config.toml"), []byte(cfg), 0o644)
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "init: seed memory repo"}} {
		if out, err := git(memPath, args...); err != nil {
			return fmt.Errorf("git %v: %v: %s", args, err, out)
		}
	}
	return nil
}

func git(repo string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
	return string(out), err
}

// CommitAll stages and commits the whole tree: one logical op = one commit.
func CommitAll(memPath, msg string) error {
	if _, err := git(memPath, "add", "-A"); err != nil {
		return err
	}
	if out, _ := git(memPath, "status", "--porcelain"); strings.TrimSpace(out) == "" {
		return nil // nothing changed
	}
	out, err := git(memPath, "commit", "-q", "-m", msg)
	if err != nil {
		return fmt.Errorf("git commit: %v: %s", err, out)
	}
	return nil
}

// HasEchoCommit reports whether an echo commit references the session id.
func HasEchoCommit(memPath, sessionID string) bool {
	out, err := git(memPath, "log", "--grep", sessionID, "--oneline")
	return err == nil && strings.TrimSpace(out) != ""
}

// WriteEntry renders an entry into the scope's entries/ dir.
func WriteEntry(memPath, project string, e *Entry) (string, error) {
	dir := filepath.Join(ProjectDir(memPath, project), "entries")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	date := strings.ReplaceAll(e.Created, "-", "")
	name := fmt.Sprintf("%s-%s.md", date, Slug(e.Title))
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil { // slug collision
		path = filepath.Join(dir, fmt.Sprintf("%s-%s-%s.md", date, Slug(e.Title), strings.ToLower(e.ShortID())))
	}
	return path, os.WriteFile(path, []byte(e.Render()), 0o644)
}

// LoadEntries reads + validates all entries for a project, quarantining
// invalid ones to archive/invalid/ (never silently dropped).
func LoadEntries(memPath, project string) (entries []*Entry, quarantined []string, err error) {
	dir := filepath.Join(ProjectDir(memPath, project), "entries")
	files, _ := filepath.Glob(filepath.Join(dir, "*.md"))
	sort.Strings(files)
	for _, f := range files {
		b, rerr := os.ReadFile(f)
		if rerr != nil {
			continue
		}
		e, perr := Parse(string(b))
		var problems []string
		if perr != nil {
			problems = []string{perr.Error()}
		} else {
			problems = e.Validate()
		}
		if len(problems) > 0 {
			inv := filepath.Join(ProjectDir(memPath, project), "archive", "invalid")
			os.MkdirAll(inv, 0o755)
			os.Rename(f, filepath.Join(inv, filepath.Base(f)))
			quarantined = append(quarantined, fmt.Sprintf("%s: %s", filepath.Base(f), strings.Join(problems, "; ")))
			continue
		}
		e.FilePath = f
		entries = append(entries, e)
	}
	return entries, quarantined, nil
}

// Questions returns open items from questions.md, newest first (list items
// in file order are oldest-first; the file is append-only).
func Questions(memPath, project string) []string {
	b, err := os.ReadFile(filepath.Join(ProjectDir(memPath, project), "questions.md"))
	if err != nil {
		return nil
	}
	var items []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- ") {
			items = append(items, strings.TrimSpace(line))
		}
	}
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
	return items
}
