// Package echo implements distillation (plan §3.7–3.8): transcript in,
// validated entries + session log out, one commit.
package echo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/oklog/ulid/v2"

	"github.com/aliirz/raqim/internal/config"
	"github.com/aliirz/raqim/internal/memory"
	"github.com/aliirz/raqim/internal/provider"
	"github.com/aliirz/raqim/internal/session"
)

// prompt is the echo prompt v0, verbatim from plan §3.8.
const prompt = `you are the echo for raqim, a coding harness with persistent memory. you
receive the full transcript of one coding session. extract only durable
knowledge worth carrying to future sessions.

entry types: decision (choice made + why) · gotcha (footgun + avoidance) ·
insight (transferable pattern) · reference (external fact: api limit, vendor
quirk) · task (open thread with concrete next step).

rules:
- fewer, better. 0 entries is a valid output for a routine session. >5 needs
  exceptional justification.
- never narrate the session ("we fixed the tests"). extract what a future
  session needs to know that it couldn't rediscover cheaply.
- first paragraph of each body ≤120 words, self-contained.
- NEVER include credentials, tokens, env values, or anything resembling a
  secret, even partially redacted.
- anchors: list the repo-relative file paths this knowledge is tied to, chosen
  from the touched-files list provided. omit if genuinely unanchored.
- if the session contradicts or supersedes likely existing memory, say so in
  the entry body ("supersedes any earlier guidance that...").

also produce "summary": ≤5 lines of what happened, for the session log.

output strictly this json, nothing else:
{"summary": "...",
 "entries": [{"type":"gotcha","title":"...","body":"...","anchors":["path"],
              "tags":["..."],"expires":null}]}`

type rawEntry struct {
	Type    string   `json:"type"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Anchors []string `json:"anchors"`
	Tags    []string `json:"tags"`
	Expires *string  `json:"expires"`
}

type rawOutput struct {
	Summary string     `json:"summary"`
	Entries []rawEntry `json:"entries"`
}

// Run distills one session transcript into the memory repo.
func Run(ctx context.Context, prov provider.Provider, cfg *config.Config, sessionID string) error {
	path, project, err := session.Find(sessionID)
	if err != nil {
		return err
	}
	evs, err := session.Load(path)
	if err != nil {
		return err
	}
	memPath := cfg.MemoryPath()
	shaMap := touchedFiles(evs, cfg.Projects[project])

	transcript := renderTranscript(evs)
	input := fmt.Sprintf("touched files (repo-relative, with repo head sha):\n%s\n\nfull session transcript:\n%s",
		renderShaMap(shaMap), transcript)

	out, verrs, err := callAndValidate(ctx, prov, cfg.Model.Echo, input, "")
	if err != nil {
		return err
	}
	if len(verrs) > 0 { // retry once with the validation errors appended
		out, verrs, err = callAndValidate(ctx, prov, cfg.Model.Echo, input,
			"your previous output failed validation:\n- "+strings.Join(verrs, "\n- "))
		if err != nil {
			return err
		}
	}
	date := time.Now().Format("2006-01-02")
	logDir := filepath.Join(memory.ProjectDir(memPath, project), "log")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	if len(verrs) > 0 { // second failure: write raw output, flag at startup
		undist := filepath.Join(logDir, fmt.Sprintf("%s-%s-UNDISTILLED.md", date, sessionID))
		body := fmt.Sprintf("# undistilled echo output\n\nvalidation errors:\n- %s\n\nraw output:\n\n%s\n",
			strings.Join(verrs, "\n- "), out.raw)
		os.WriteFile(undist, []byte(body), 0o644)
		memory.CommitAll(memPath, fmt.Sprintf("echo(%s): UNDISTILLED %s", project, sessionID))
		return fmt.Errorf("echo output failed validation twice; raw saved to %s", undist)
	}

	for _, re := range out.parsed.Entries {
		e := &memory.Entry{
			ID: ulid.Make().String(), Type: re.Type, Title: re.Title, Status: "active",
			Created: date, Updated: date, Author: "echo", Source: []string{sessionID},
			Tags: re.Tags, Body: re.Body, Anchors: resolveAnchors(re.Anchors, shaMap),
		}
		if re.Expires != nil {
			e.Expires = *re.Expires
		}
		if _, err := memory.WriteEntry(memPath, project, e); err != nil {
			return err
		}
	}
	logPath := filepath.Join(logDir, fmt.Sprintf("%s-%s.md", date, sessionID))
	os.WriteFile(logPath, []byte(fmt.Sprintf("# session %s\n\n%s\n", sessionID, strings.TrimSpace(out.parsed.Summary))), 0o644)

	mc := config.LoadMemConfig(memPath)
	if _, err := memory.WriteIndex(memPath, project, mc.Budgets.IndexTokens); err != nil {
		return err
	}
	msg := fmt.Sprintf("echo(%s): %d entries from %s", project, len(out.parsed.Entries), sessionID)
	if err := memory.CommitAll(memPath, msg); err != nil {
		return err
	}
	fmt.Printf("[echo] %s\n", msg)
	return nil
}

type echoOut struct {
	raw    string
	parsed rawOutput
}

func callAndValidate(ctx context.Context, prov provider.Provider, model, input, errNote string) (echoOut, []string, error) {
	msgs := []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(input))}
	if errNote != "" {
		msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(errNote+"\noutput the corrected json, nothing else.")))
	}
	resp, err := prov.Complete(ctx, model, []anthropic.TextBlockParam{{Text: prompt}}, msgs, nil, 8192)
	if err != nil {
		return echoOut{}, nil, err
	}
	var raw string
	for _, b := range resp.Content {
		if t, ok := b.AsAny().(anthropic.TextBlock); ok {
			raw += t.Text
		}
	}
	parsed, verrs := ValidateOutput(raw)
	return echoOut{raw: raw, parsed: parsed}, verrs, nil
}

// ValidateOutput parses and schema-checks echo json (plan §3.7).
func ValidateOutput(raw string) (rawOutput, []string) {
	var out rawOutput
	jsonStr := raw
	if i := strings.Index(jsonStr, "{"); i >= 0 {
		jsonStr = jsonStr[i : strings.LastIndex(jsonStr, "}")+1]
	}
	if err := json.Unmarshal([]byte(jsonStr), &out); err != nil {
		return out, []string{"not valid json: " + err.Error()}
	}
	var errs []string
	if strings.TrimSpace(out.Summary) == "" {
		errs = append(errs, "missing summary")
	}
	for i, e := range out.Entries {
		at := fmt.Sprintf("entries[%d]", i)
		if !memory.Types[e.Type] {
			errs = append(errs, at+": bad type "+e.Type)
		}
		if e.Title == "" || len(e.Title) > 80 {
			errs = append(errs, fmt.Sprintf("%s: title must be 1-80 chars, got %d", at, len(e.Title)))
		}
		first := strings.Split(strings.TrimSpace(e.Body), "\n\n")[0]
		if n := len(strings.Fields(first)); n == 0 || n > 120 {
			errs = append(errs, fmt.Sprintf("%s: first paragraph must be 1-120 words, got %d", at, n))
		}
	}
	return out, errs
}

// touchedFiles extracts file paths from tool calls and maps them to
// repo-relative paths with the repo's current head sha.
func touchedFiles(evs []session.Event, repoPaths []string) map[string]memory.Anchor {
	out := map[string]memory.Anchor{}
	for _, ev := range evs {
		if ev.T != "tool_call" || (ev.Name != "read" && ev.Name != "write" && ev.Name != "edit") {
			continue
		}
		var m struct{ Path string }
		if json.Unmarshal(ev.Args, &m) != nil || m.Path == "" {
			continue
		}
		for _, rp := range repoPaths {
			root := config.ExpandTilde(rp)
			abs := m.Path
			if !filepath.IsAbs(abs) {
				abs = filepath.Join(root, abs)
			}
			rel, err := filepath.Rel(root, abs)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			sha := headSHA(root)
			out[rel] = memory.Anchor{Repo: filepath.Base(root), Path: rel, SHA: sha}
			break
		}
	}
	return out
}

func headSHA(repo string) string {
	b, err := exec.Command("git", "-C", repo, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func resolveAnchors(paths []string, shaMap map[string]memory.Anchor) []memory.Anchor {
	var out []memory.Anchor
	for _, p := range paths {
		if a, ok := shaMap[p]; ok {
			out = append(out, a)
		}
	}
	return out
}

func renderShaMap(m map[string]memory.Anchor) string {
	if len(m) == 0 {
		return "(none)"
	}
	var lines []string
	for _, a := range m {
		lines = append(lines, fmt.Sprintf("%s (repo %s @ %s)", a.Path, a.Repo, a.SHA))
	}
	return strings.Join(lines, "\n")
}

// renderTranscript flattens jsonl events for the echo model. Echo always
// reads the full, uncompacted transcript (invariant §7).
func renderTranscript(evs []session.Event) string {
	var b strings.Builder
	for _, ev := range evs {
		switch ev.T {
		case "user":
			fmt.Fprintf(&b, "[user] %s\n", ev.Text)
		case "assistant":
			fmt.Fprintf(&b, "[assistant] %s\n", ev.Text)
		case "tool_call":
			fmt.Fprintf(&b, "[tool_call %s] %s %s\n", ev.ID, ev.Name, ev.Args)
		case "tool_result":
			status := "ok"
			if ev.OK != nil && !*ev.OK {
				status = "error"
			}
			fmt.Fprintf(&b, "[tool_result %s %s] %s\n", ev.ID, status, ev.Output)
		case "end":
			fmt.Fprintf(&b, "[end] reason=%s\n", ev.Reason)
		}
	}
	return b.String()
}
