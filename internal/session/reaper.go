package session

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EchoCheck reports whether an echo commit references a session id
// (provided by memory.HasEchoCommit; injected to avoid the import).
type EchoCheck func(sessionID string) bool

// Reap deletes transcripts older than ttlDays IF an echo commit references
// them (plan §3.11). Undistilled + expired transcripts are warned about,
// never silently deleted: distill_before_delete is the invariant.
func Reap(ttlDays int, distilled EchoCheck, out io.Writer) {
	cutoff := time.Now().AddDate(0, 0, -ttlDays)
	for _, paths := range All() {
		for _, p := range paths {
			info, err := os.Stat(p)
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
			if distilled(id) {
				os.Remove(p)
				fmt.Fprintf(out, "[reaper] deleted distilled transcript %s (older than %dd)\n", id, ttlDays)
			} else {
				fmt.Fprintf(out, "[reaper] %s expired but undistilled — kept. distill with: raqim echo %s\n", id, id)
			}
		}
	}
}

// Undistilled returns sessions needing recovery (plan §3.7): missing end
// event or no referencing echo commit. Keyed by session id → project.
func Undistilled(distilled EchoCheck) map[string]string {
	out := map[string]string{}
	for project, paths := range All() {
		for _, p := range paths {
			id := strings.TrimSuffix(filepath.Base(p), ".jsonl")
			if distilled(id) {
				continue
			}
			out[id] = project
		}
	}
	return out
}
