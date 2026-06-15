# RAQIM.md

raqim's own project memory doc — dogfooded from day 1 (plan §1).

gen-0 kernel built 2026-06-13 by claude code against
docs/implementation-plan.md v1.0. the persistent memory for this project
lives in the memory repo (`~/.raqim/memory/projects/raqim/`); this file is
the in-repo pointer to it.

known gen-0 deviations from the plan:
- `memory_search` is a pure-go scan, not ripgrep (the build machine has no
  `rg` binary). same tool schema and result shape; gen-1 replaces it with
  sqlite fts per the plan either way.

gen-0 line count: ~1,580 effective lines vs the plan's ~800 target —
accepted as-is (human decision, 2026-06-13).

gen-1 dogfood tasks, in order:
1. ✓ compaction (plan §4.1) — completed 2026-06-13.
   - `internal/compaction`: Compactor, tailTurns (exchange-atomic, tool-use
     safe), ShouldTrigger (85% trigger / 60% rearm), Run, AssembleProviderView.
   - `internal/session`: Compaction event.
   - `internal/agent`: turnMsgs split, mid-tool-use guard (StopReason check).
   commits: feat: compaction core · fix: mid-tool-use guard + re-arm
2. kernel slimming — bring gen-0 back toward the ~800-line target.
3. ✓ input handling — completed 2026-06-14.
   - `cmd/raqim`: `readInput` coalesces bracketed-paste sequences
     (ESC[200~…ESC[201~) into one turn; `[paste: N lines]` boundary marker.
   - startup emits `\x1b[?2004h` to enable paste mode; all exit paths
     (defer + signal handler) emit `\x1b[?2004l` to disable.
   - `internal/echo`: `extractJSON` + `topLevelObjects` replace the
     first-{-to-last-} slicer; handles ```json fences and multi-object
     self-corrections; picks last valid {summary,entries} shape.

open cosmetic:
- bracketed-paste markers echo as literal ^[[200~/^[[201~ in the input
  display; strip from echoed input — parsing is already correct, only the
  on-screen echo is affected.

backlog:
- auto-continue on max_tokens: the turn loop currently halts and returns
  ErrMaxTokens when the response is truncated. for unattended/self-build
  sessions, the loop should instead append the synthetic tool_result and
  re-call Complete() so the model can finish in smaller steps without user
  intervention. the synthetic guidance text ("produce a smaller output or
  split the work into steps") is already in place; the loop just needs to
  not return on max_tokens when a tool_use was in flight. guarded by
  ErrMaxTokens sentinel + the comment in internal/agent/agent.go.
