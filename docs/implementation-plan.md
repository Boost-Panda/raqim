# raqim implementation plan v1.0

companion docs: `PHILOSOPHY.md`, `memory-spec.md` (v0.2). this document is the
build plan. gen-0 is built by claude code tonight against this plan. every
generation after is built by raqim itself.

## 0. the generation model

- gen-0 (tonight): kernel, built by claude code. functional, ugly, complete
  enough to self-host.
- gen-1 (week 1): raqim builds compaction, /remember, sqlite index, staleness.
- gen-2+: dream engine, provider layer, TUI.
- rules during self-hosting: human reviews every code diff before rebuild.
  human skims every echo commit in week 1. the memory repo's git log is the
  instrument panel for whether the loop is converging.

## 1. repo layout (go)

```
raqim/
├── cmd/raqim/main.go        # cli entry, subcommands
├── internal/
│   ├── agent/               # loop, turn handling, usage tracking
│   ├── provider/            # Provider iface + anthropic impl (only one in gen-0)
│   ├── tools/               # read, write, edit, bash, memory_search
│   ├── permission/          # prompt engine, session grants, denylist
│   ├── session/             # jsonl read/write, ids, reaper
│   ├── memory/              # tree io, entry parse/validate, index gen, inject
│   ├── echo/              # distillation: prompt, retry loop, render, commit
│   └── config/              # ~/.raqim/config.toml + memory/config.toml
├── PHILOSOPHY.md
├── docs/memory-spec.md
└── RAQIM.md                 # raqim's own project memory doc, dogfood from day 1
```

subcommands gen-0: `raqim` (interactive), `raqim run -p "..."` (one-shot),
`raqim echo <session-id>` (manual distill), `raqim reindex`.
reserved: `raqim dream`, `raqim memory`.

## 2. gen-0 kernel scope

IN: blocking agent loop · anthropic api (hardcoded model from config) · tools:
read/write/edit/bash/memory_search · permission engine · jsonl sessions ·
injection on start · echo on end · undistilled-session recovery · MEMORY.md
generation · ttl reaper · context warn at 80%.

OUT (do not build tonight): TUI, streaming, compaction, providers other than
anthropic, tool-call repair, sqlite, staleness checker, dream, /remember,
subagents.

target: ~800 lines. if it's heading past 1200, cut scope, not corners.

## 3. component specs

### 3.1 config (~/.raqim/config.toml)

```toml
[model]
agent  = "claude-sonnet-4-6"      # gen-0: anthropic only
echo = "claude-haiku-4-5"

[projects]
saleskick = ["~/code/saleskick", "~/code/saleskick-lns"]   # n repos : 1 project
tra       = ["~/code/venuesumo-sync"]

[memory]
path = "~/.raqim/memory"

[context]
warn_pct = 80
```

unknown git root on startup → prompt "map this repo to a project (new or
existing)" → append to config. memory repo auto `git init` on first run,
seeded with global/docs/profile.md stub + projects/ dirs. remote sync is
manual, out of scope.

### 3.2 project identity

resolve: cwd → `git rev-parse --show-toplevel` → match against [projects]
(tilde-expanded). no git repo → project "scratch". the project name selects
the memory scope for injection, echo, and memory_search default.

### 3.3 session jsonl

path: `~/.raqim/sessions/<project>/s-YYYYMMDD-<4hex>.jsonl`. one event per line:

```jsonl
{"t":"meta","session":"s-20260612-a4f2","project":"saleskick","model":"claude-sonnet-4-6","gen":0,"started":"<rfc3339>"}
{"t":"user","ts":"...","text":"..."}
{"t":"assistant","ts":"...","text":"..."}
{"t":"tool_call","id":"tc_1","name":"bash","args":{"command":"go test ./..."}}
{"t":"tool_result","id":"tc_1","ok":true,"output":"...","truncated":false}
{"t":"usage","in":1234,"out":567,"cache_read":8900,"ctx_pct":41}
{"t":"end","reason":"exit","ts":"..."}        # exit | interrupt | context_limit | error
```

reserved event types for later gens: `compaction`, `subagent`, `model_switch`.
tool outputs are capped (default 16k chars, tail-truncated, `"truncated":true`).
ctrl-c handler writes the `end` event before dying; a missing `end` event also
marks a session undistilled.

### 3.4 permission engine

every tool call except `read` and `memory_search` prompts:

```
bash: go test ./internal/...
[y] run once  [n] deny + tell raqim why  [a] allow all bash this session
```

- grants are per tool class: `bash` and `mutate` (write+edit) approve separately
- grants are session-scoped, never persisted
- denylist always applies, even under allow-all: `rm -rf` outside cwd,
  `git push --force*`, any path under ~/.raqim, sudo. denied → tool_result
  with ok:false and the reason, agent sees it and adapts
- `[n]` prompts one line of feedback, returned as the tool result

### 3.5 injection (session start)

assemble in order, byte-stable for the session (this is the cache prefix):
1. global/docs/profile.md
2. global/docs/entities.md (project-tagged entries only, when tags exist)
3. projects/<name>/MEMORY.md + staleness annotations (gen-1; gen-0 injects as-is)
4. open questions.md items, max 5, newest first
budget 3000 tokens; over-budget → drop (4), then truncate (3) per its own rules.
empty memory → inject bootstrap block: "memory is empty for this project.
durable facts you establish this session will be distilled and carried forward."

### 3.6 agent loop (gen-0)

blocking request/response, no streaming. system prompt = static raqim prompt
(<800 tokens: identity, tool guidance, memory note) + injection block with
anthropic cache_control breakpoint after it. loop: send → tool calls? execute
serially with permission → append results → repeat. text-only response →
print, return to readline. track ctx_pct from response usage; ≥warn_pct →
print warning once: "context at N%. consider /exit; memory will carry context
to a fresh session." hard stop at 95% → end reason context_limit → echo runs.

### 3.7 echo

trigger: clean exit, context_limit, or `raqim echo <id>`. startup recovery:
scan for sessions with no `end` event or no referencing echo commit → offer
batch distill.

call: echo model, full transcript (uncompacted, always), file→sha map for
every file touched this session (harness computes from the project repos), and
the echo prompt (3.8). expected output: json.

validate: parse json → schema-check each entry (required fields, type enum,
title ≤80 chars, first para ≤120 words) → on failure, retry once with the
validation errors appended → on second failure, write raw output to
log/<date>-<session>-UNDISTILLED.md and flag in next startup.

render: each entry → entries/YYYYMMDD-<slug>.md with frontmatter per
memory-spec (ulid assigned by harness, author: echo, source: [session-id],
anchors from the file→sha map). write log/<date>-<session>.md (echo's 5-line
session summary). regenerate MEMORY.md. single commit:
`echo(<project>): N entries from <session-id>`.

### 3.8 echo prompt v0

```
you are the echo for raqim, a coding harness with persistent memory. you
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
              "tags":["..."],"expires":null}]}
```

### 3.9 MEMORY.md generation

deterministic render from active entries + docs/ listing, per memory-spec §4:
fixed section order, `title {short-id} (mm-dd)` lines, 1500-token budget with
per-section newest-N truncation. invoked by echo, dream, and `raqim reindex`.

### 3.10 memory_search (gen-0)

ripgrep over the memory tree: `rg -i --json <query>` scoped to global/ +
projects/<current>/, excluding archive/. returns path, short-id (parsed from
frontmatter), title, first-paragraph excerpt. max 10 hits. replaced by sqlite
fts in gen-1 behind the same tool schema.

### 3.11 ttl reaper

on startup: delete transcripts older than [sessions].ttl_days IF an echo
commit referencing the session id exists in the memory repo (git log --grep).
undistilled + expired → warn, never silently delete. distill_before_delete is
the invariant; provider-down must not cause data loss.

## 4. gen-1 backlog (built by raqim, in order)

1. **compaction.** trigger 85% (provider-reported usage). agent model writes
   structured handoff (task state, decisions, files touched, next steps).
   provider view becomes: injection block (verbatim, cache prefix preserved) +
   handoff + last 3 turns. jsonl unaffected; `compaction` event recorded;
   echo still reads everything. acceptance: a session crossing 85% continues
   seamlessly and the post-session echo output is unaffected.
2. **/remember.** queues human note; echo must emit it as author: human entry.
3. **sqlite fts index** per memory-spec §6, behind the existing memory_search
   schema. incremental on write, rebuilt on reindex.
4. **staleness checker** per memory-spec §3, run at inject + manual
   `raqim memory verify`.
5. **echo prompt versioning**: prompt file lives in memory repo, version in
   echo commit message.

## 5. gen-2+ backlog

dream engine (map-reduce replay: chunk by project then time, two levels max ·
consolidation · anchor-based pruning · promotion on 2+ project evidence ·
questions.md with n-cycle expiry · budget cap · one commit) → provider layer
(neutral message format, openai-compat adapter for openrouter+ollama, model
registry, manual /model switch only, tool-call repair: schema-validate, feed
errors back, 2 retries, json-in-text fallback for no-tool models) → TUI
(bubble tea, event-stream consumer, jade).

## 6. tonight's runbook

1. repo init, this doc + philosophy + memory-spec committed first (spec-first,
   it's the case study).
2. hand claude code this file. build order: config → session → permission →
   tools → provider → agent loop → memory io + index gen → echo → reaper →
   cli wiring.
3. acceptance gate, in order:
   a. `raqim` in a mapped repo holds a multi-turn conversation with tool use
      and permission prompts.
   b. exit → echo commit appears in memory repo with valid entries.
   c. new session → MEMORY.md content visibly informs the model.
   d. kill -9 a session → next startup offers recovery distill.
   e. `raqim reindex` is a no-op diff on a clean tree.
4. first real dogfood task for gen-1: "raqim, implement compaction per
   section 4.1 of your implementation plan." review the diff. rebuild.
   you are now self-hosting.

## 7. invariants (carry from memory-spec, enforced in code review)

tree is truth · every mutation one commit · scope = path · raw ephemeral,
distilled permanent · injection immutable per session · echo reads
uncompacted · machines don't overwrite humans · zero telemetry · archive,
never delete.
