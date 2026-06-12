// Package tools implements the gen-0 tool set: read, write, edit, bash,
// memory_search (plan §2, §3.10).
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
)

type Ctx struct {
	Cwd        string
	MemoryPath string
	Project    string
	Touched    map[string]bool // files read/written this session, for echo anchors
}

func obj(props map[string]any, required ...string) anthropic.ToolInputSchemaParam {
	return anthropic.ToolInputSchemaParam{Properties: props, Required: required}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

// Definitions returns the tool schemas sent to the provider.
func Definitions() []anthropic.ToolUnionParam {
	defs := []anthropic.ToolParam{
		{Name: "read", Description: anthropic.String("Read a file. Returns its full contents."),
			InputSchema: obj(map[string]any{"path": str("absolute or cwd-relative file path")}, "path")},
		{Name: "write", Description: anthropic.String("Write content to a file, creating parent dirs. Overwrites."),
			InputSchema: obj(map[string]any{"path": str("file path"), "content": str("full file content")}, "path", "content")},
		{Name: "edit", Description: anthropic.String("Replace an exact unique string in a file."),
			InputSchema: obj(map[string]any{"path": str("file path"), "old": str("exact text to replace (must occur exactly once)"), "new": str("replacement text")}, "path", "old", "new")},
		{Name: "bash", Description: anthropic.String("Run a shell command in the project directory. Returns combined output."),
			InputSchema: obj(map[string]any{"command": str("the command to run")}, "command")},
		{Name: "memory_search", Description: anthropic.String("Search persistent memory (global + this project) for durable facts from past sessions. Use before re-deriving anything that may already be known."),
			InputSchema: obj(map[string]any{"query": str("search terms")}, "query")},
	}
	out := make([]anthropic.ToolUnionParam, len(defs))
	for i := range defs {
		out[i] = anthropic.ToolUnionParam{OfTool: &defs[i]}
	}
	return out
}

// Summary renders the human-readable line shown in permission prompts.
func Summary(name string, args json.RawMessage) string {
	var m map[string]string
	json.Unmarshal(args, &m)
	switch name {
	case "bash":
		return m["command"]
	case "read", "write", "edit":
		return m["path"]
	default:
		return m["query"]
	}
}

// Execute runs a tool. ok=false results are still returned to the model.
func Execute(c *Ctx, name string, args json.RawMessage) (output string, ok bool) {
	var m struct{ Path, Content, Old, New, Command, Query string }
	if err := json.Unmarshal(args, &m); err != nil {
		return "bad tool arguments: " + err.Error(), false
	}
	abs := func(p string) string {
		if filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(c.Cwd, p)
	}
	switch name {
	case "read":
		b, err := os.ReadFile(abs(m.Path))
		if err != nil {
			return err.Error(), false
		}
		c.Touched[abs(m.Path)] = true
		return string(b), true
	case "write":
		if err := os.MkdirAll(filepath.Dir(abs(m.Path)), 0o755); err != nil {
			return err.Error(), false
		}
		if err := os.WriteFile(abs(m.Path), []byte(m.Content), 0o644); err != nil {
			return err.Error(), false
		}
		c.Touched[abs(m.Path)] = true
		return fmt.Sprintf("wrote %d bytes to %s", len(m.Content), m.Path), true
	case "edit":
		b, err := os.ReadFile(abs(m.Path))
		if err != nil {
			return err.Error(), false
		}
		if n := strings.Count(string(b), m.Old); n != 1 {
			return fmt.Sprintf("old string occurs %d times, need exactly 1", n), false
		}
		if err := os.WriteFile(abs(m.Path), []byte(strings.Replace(string(b), m.Old, m.New, 1)), 0o644); err != nil {
			return err.Error(), false
		}
		c.Touched[abs(m.Path)] = true
		return "edited " + m.Path, true
	case "bash":
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "-c", m.Command)
		cmd.Dir = c.Cwd
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out) + "\n" + err.Error(), false
		}
		return string(out), true
	case "memory_search":
		return MemorySearch(c.MemoryPath, c.Project, m.Query)
	}
	return "unknown tool: " + name, false
}

// MemorySearch is the gen-0 search (plan §3.10): a case-insensitive scan
// of global/ + projects/<current>/, excluding archive/. Implemented as a
// pure-Go walk (this machine has no ripgrep); same tool schema and result
// shape, swapped for sqlite fts in gen-1.
func MemorySearch(memPath, project, query string) (string, bool) {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return "empty query", false
	}
	type hit struct {
		path, shortID, title, excerpt string
		score                         int
	}
	var hits []hit
	roots := []string{filepath.Join(memPath, "global"), filepath.Join(memPath, "projects", project)}
	for _, root := range roots {
		filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				if info != nil && info.IsDir() && info.Name() == "archive" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(p, ".md") || info.Name() == "MEMORY.md" {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			lower := strings.ToLower(string(b))
			score := 0
			for _, t := range terms {
				score += strings.Count(lower, t)
			}
			if score == 0 {
				return nil
			}
			id, title, excerpt := scanEntry(string(b))
			rel, _ := filepath.Rel(memPath, p)
			hits = append(hits, hit{rel, id, title, excerpt, score})
			return nil
		})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > 10 {
		hits = hits[:10]
	}
	if len(hits) == 0 {
		return "no matches in memory", true
	}
	var sb strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&sb, "%s {%s}\n  %s\n  %s\n", h.path, h.shortID, h.title, h.excerpt)
	}
	return sb.String(), true
}

// scanEntry pulls short-id, title, and first body paragraph from an entry
// (or any markdown file; id/title empty when no frontmatter).
func scanEntry(content string) (shortID, title, excerpt string) {
	body := content
	if strings.HasPrefix(content, "---\n") {
		if end := strings.Index(content[4:], "\n---"); end >= 0 {
			fm := content[4 : 4+end]
			body = content[4+end+4:]
			for _, line := range strings.Split(fm, "\n") {
				if v, ok := strings.CutPrefix(line, "id: "); ok && len(v) >= 7 {
					shortID = strings.TrimSpace(v)[:7]
				}
				if v, ok := strings.CutPrefix(line, "title: "); ok {
					title = strings.TrimSpace(v)
				}
			}
		}
	}
	for _, para := range strings.Split(strings.TrimSpace(body), "\n\n") {
		para = strings.TrimSpace(para)
		if para != "" && !strings.HasPrefix(para, "#") {
			excerpt = strings.ReplaceAll(para, "\n", " ")
			if len(excerpt) > 300 {
				excerpt = excerpt[:300] + "…"
			}
			break
		}
	}
	return
}
