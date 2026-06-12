# raqim
a coding harness with persistent memory. go, single binary.

read before any work, in order:
1. PHILOSOPHY.md — non-negotiable principles
2. docs/memory-spec.md — the memory wire protocol (v0.2)
3. docs/implementation-plan.md — THE build plan. follow it exactly.

hard rules:
- gen-0 scope is plan §2. anything in the OUT list: do not build it,
  even if it seems easy. stub interfaces only.
- invariants in plan §7 override any implementation convenience.
- target ~800 lines. past 1200, stop and ask before continuing.
- no third-party deps beyond anthropic-sdk-go, a toml parser, and a
  ulid lib. stdlib otherwise.
- table-driven tests for: entry frontmatter validation, MEMORY.md
  generation determinism, permission denylist, jsonl round-trip.
- never use bare `echo` in shell examples or docs about the echo
  subsystem; disambiguate as "the echo pass" or `raqim echo`.
- never delete anything under ~/.raqim/sessions or ~/.raqim/memory;
  the reaper is the only deletion path.
