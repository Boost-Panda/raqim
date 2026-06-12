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

first dogfood task for gen-1 (plan §6.4): "raqim, implement compaction per
section 4.1 of your implementation plan."
