package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// tokens approximates token count for budget checks (chars/4).
func tokens(s string) int { return len(s) / 4 }

var sectionOrder = []string{"decision", "gotcha", "insight", "reference", "task"}

var sectionTitle = map[string]string{
	"decision": "decisions", "gotcha": "gotchas", "insight": "insights",
	"reference": "references", "task": "tasks",
}

// GenIndex deterministically renders projects/<p>/MEMORY.md from active
// entries + docs listing (memory-spec §4). Returns the rendered content.
func GenIndex(memPath, project string, budgetTokens int) (string, error) {
	entries, quarantined, err := LoadEntries(memPath, project)
	if err != nil {
		return "", err
	}
	for _, q := range quarantined {
		fmt.Fprintf(os.Stderr, "[memory] quarantined invalid entry: %s\n", q)
	}
	var active []*Entry
	for _, e := range entries {
		if e.Status != "superseded" {
			active = append(active, e)
		}
	}
	bySection := map[string][]*Entry{}
	for _, e := range active {
		bySection[e.Type] = append(bySection[e.Type], e)
	}
	for _, es := range bySection {
		sort.Slice(es, func(i, j int) bool { // newest first, id tiebreak for determinism
			if es[i].Created != es[j].Created {
				return es[i].Created > es[j].Created
			}
			return es[i].ID > es[j].ID
		})
	}

	docs := docLines(memPath, project)
	nq := len(Questions(memPath, project))

	// shrink per-section cap until under budget (truncation should be rare)
	for cap := 50; cap >= 1; cap-- {
		out := render(project, len(active), nq, docs, bySection, cap)
		if tokens(out) <= budgetTokens || cap == 1 {
			return out, nil
		}
	}
	return "", nil // unreachable
}

func render(project string, nActive, nQuestions int, docs []string, bySection map[string][]*Entry, cap int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — memory index\n", project)
	fmt.Fprintf(&b, "> %d active · %d open questions\n", nActive, nQuestions)
	if len(docs) > 0 {
		b.WriteString("\n## documents\n")
		for _, d := range docs {
			b.WriteString(d + "\n")
		}
	}
	for _, typ := range sectionOrder {
		es := bySection[typ]
		if len(es) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s\n", sectionTitle[typ])
		shown := es
		if len(shown) > cap {
			shown = shown[:cap]
		}
		for _, e := range shown {
			date := mmdd(e.Created)
			if typ == "task" && e.Expires != "" {
				date = "due " + mmdd(e.Expires)
			}
			fmt.Fprintf(&b, "- %s {%s} (%s)\n", e.Title, e.ShortID(), date)
		}
		if len(es) > cap {
			fmt.Fprintf(&b, "… +%d more, use memory_search\n", len(es)-cap)
		}
	}
	return b.String()
}

func mmdd(date string) string {
	if len(date) == 10 { // YYYY-MM-DD
		return date[5:]
	}
	return date
}

func docLines(memPath, project string) []string {
	files, _ := filepath.Glob(filepath.Join(ProjectDir(memPath, project), "docs", "*.md"))
	sort.Strings(files)
	var lines []string
	for _, f := range files {
		desc := ""
		if b, err := os.ReadFile(f); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if h, ok := strings.CutPrefix(l, "# "); ok {
					desc = " — " + strings.TrimSpace(h)
					break
				}
			}
		}
		lines = append(lines, fmt.Sprintf("- docs/%s%s", filepath.Base(f), desc))
	}
	return lines
}

// WriteIndex regenerates and writes MEMORY.md, returning true if changed.
func WriteIndex(memPath, project string, budgetTokens int) (bool, error) {
	content, err := GenIndex(memPath, project, budgetTokens)
	if err != nil {
		return false, err
	}
	path := filepath.Join(ProjectDir(memPath, project), "MEMORY.md")
	old, _ := os.ReadFile(path)
	if string(old) == content {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(content), 0o644)
}

// Inject assembles the session-start injection block (plan §3.5), byte-
// stable for the session. Order: profile, entities, MEMORY.md, questions.
func Inject(memPath, project string, budgetTokens int) string {
	read := func(rel string) string {
		b, _ := os.ReadFile(filepath.Join(memPath, rel))
		return strings.TrimSpace(string(b))
	}
	profile := read(filepath.Join("global", "docs", "profile.md"))
	entities := read(filepath.Join("global", "docs", "entities.md"))
	index := read(filepath.Join("projects", project, "MEMORY.md"))

	var parts []string
	if profile != "" {
		parts = append(parts, profile)
	}
	if entities != "" {
		parts = append(parts, entities)
	}
	if index == "" {
		parts = append(parts, fmt.Sprintf("memory is empty for project %q. durable facts you establish this session will be distilled and carried forward.", project))
	} else {
		parts = append(parts, index)
	}

	qs := Questions(memPath, project)
	if len(qs) > 5 {
		qs = qs[:5]
	}
	withQ := parts
	if len(qs) > 0 {
		withQ = append(append([]string{}, parts...), "## open questions\n"+strings.Join(qs, "\n"))
	}
	out := strings.Join(withQ, "\n\n---\n\n")
	if tokens(out) > budgetTokens { // over budget: drop questions first
		out = strings.Join(parts, "\n\n---\n\n")
	}
	if tokens(out) > budgetTokens { // then truncate the index tail
		out = out[:budgetTokens*4] + "\n[injection truncated]"
	}
	return out
}
