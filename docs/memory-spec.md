# memory format spec v0.1

the memory tree is a git repo of markdown. every component (inject, search, echo, dream, staleness checker) reads or writes this format. this document is the wire protocol.

## 1. layout

```
memory/                          # git repo root
├── config.toml                  # repo-level config (budgets, type policies)
├── global/
│   ├── docs/
│   │   ├── profile.md           # who you are, conventions, prefs
│   │   └── entities.md          # cross-project entity graph
│   └── entries/                 # atomic entries, global scope
├── projects/<name>/
│   ├── MEMORY.md                # generated index (never hand-edited)
│   ├── docs/                    # curated narrative documents
│   │   └── architecture.md
│   ├── entries/                 # atomic entries
│   ├── log/                     # echo session summaries
│   │   └── 2026-06-12-<session-id>.md
│   ├── questions.md             # dream speculation, append-only
│   └── archive/                 # retired entries, moved not deleted
└── .index/memory.db             # sqlite FTS cache, gitignored, disposable
```

two kinds of content, different rules:

- **entries**: atomic facts. one file each. machine-managed lifecycle. frontmatter is mandatory and validated.
- **documents**: narrative prose (architecture maps, profile). curated by dream or human. frontmatter optional (`updated` only). never auto-archived.

scope is derived from path (`global/` vs `projects/<name>/`). it is never stored in frontmatter; the path is the single source of truth.

## 2. entry file

filename: `YYYYMMDD-<slug>.md` (date + frozen kebab slug, ≤6 words). the canonical identity is the `id` in frontmatter, not the filename.

```markdown
---
id: 01J8FZ3K9QW2          # ulid, assigned by harness, immutable
type: gotcha               # decision | gotcha | insight | reference | task
title: twilio webhook retries are not idempotent by default
status: active             # active | needs-verify | superseded
created: 2026-06-12
updated: 2026-06-12
author: echo             # echo | dream | human
source: [s-20260612-a4f2]  # session ids this was distilled from
anchors:
  - repo: saleskick
    path: services/lns/src/webhook.ts
    sha: a1b2c3d
supersedes: 01J7XYZ...     # optional, id of replaced entry
expires: 2026-09-01        # optional, tasks mostly
tags: [whatsapp, webhooks] # optional
---
twilio fires duplicate webhook deliveries on retry with identical
MessageSid. dedup must key on MessageSid + status, not delivery.
discovered after double-writes in the activity ledger.

## evidence
- session s-20260612-a4f2, fixed in saleskick@b4e91f2
```

body contract: **first paragraph is the memory**, ≤120 words, self-contained (it's what search excerpts and what gets injected). everything after the first `##` is supporting detail, loaded only on full read.

### types and their policies

| type | what it is | dream policy |
|---|---|---|
| decision | "we chose X over Y because Z" | never auto-archives; superseded only by a newer decision |
| gotcha | footgun + how to avoid | staleness-checked hardest (anchors required) |
| insight | observed pattern, transferable lesson | candidate for promotion to global |
| reference | external fact (api limit, vendor quirk) | re-verify after 90 days |
| task | open thread, follow-up | expires aggressively; archived when done or expired |

### required fields

`id`, `type`, `title` (≤80 chars), `status`, `created`, `author`. an entry failing validation is quarantined to `archive/invalid/` at index time, never silently dropped.

### authorship rules

- **echo** may only *create* entries (and append to `log/`). it never edits or archives.
- **dream** may create, update, supersede, and archive. it may freely rewrite `author: echo` entries. it must NOT modify `author: human` entries; instead it writes a question to `questions.md` proposing the change.
- **human** edits anything. set `author: human` on entries you've corrected so dreams stop touching them.

## 3. anchors and staleness

an anchor binds an entry to code state: `repo` (logical name, mapped to a local path in harness config, never an absolute path), `path` (file or dir), `sha` (commit at write time).

check, run at inject time and during dream pruning:

1. anchored path deleted → `status: needs-verify`
2. `git diff --numstat <sha>..HEAD -- <path>` shows >40% of lines changed → `needs-verify`
3. repo not present on this machine → skip silently (multi-machine tolerance)

`needs-verify` is a flag, not a death sentence: the entry still appears in the index marked `⚠`, telling the agent to confirm against current code before relying on it. dreams resolve the flag: re-verify and either restore `active` (updating the sha) or archive.

## 4. MEMORY.md index

generated deterministically from active entries + documents on every memory write. committed (so humans and PRs can read it) but never hand-edited; `harness reindex` reproduces it byte-identically.

```markdown
# saleskick — memory index
> 47 active · last dream 2026-06-10 · 3 open questions

## documents
- docs/architecture.md — LNS, whatsapp pipeline, activity ledger (2026-06-08)

## decisions
- jsonb columns in activity ledger require schema contracts {01J8ABC} (05-30)
- zernio over twilio for whatsapp templates {01J7QRS} (04-12)

## gotchas
- twilio webhook retries are not idempotent by default {01J8FZ3} (06-12)

## tasks
- migrate lns cron jobs to monorepo scheduler {01J8DEF} (due 07-01)
```

format rules:

- one line per entry: `title {short-id} (date)`. short-id = first 7 of ulid, enough for `memory_search` and full-read lookup.
- sections in fixed order: documents, decisions, gotchas, insights, references, tasks. empty sections omitted.
- **hard budget: 1500 tokens.** when over, each section keeps its newest/most-retrieved N and ends with `… +12 more, use memory_search`. the dream's consolidation duty is to keep the full set small enough that truncation is rare.
- the committed file contains no staleness flags. flags (`⚠ verify`) are appended at inject time by the renderer, because they're computed against the *current* machine's checkouts.

## 5. injection payload

what the runtime puts in the system prompt at session start, in this order, byte-stable for the whole session (cache-friendly):

1. `global/docs/profile.md`
2. `global/docs/entities.md` (entities touching this project only, if the file has per-entity project tags)
3. project `MEMORY.md` + computed staleness annotations
4. open items from `questions.md` (max 5, newest first)

target total: ≤3000 tokens. anything dynamic discovered later in the session arrives as new messages, never by mutating this block.

## 6. sqlite index (.index/memory.db)

derived cache, gitignored, rebuildable from the tree. schema:

```sql
CREATE TABLE entries (
  id TEXT PRIMARY KEY, path TEXT, scope TEXT, type TEXT,
  title TEXT, status TEXT, created TEXT, updated TEXT,
  author TEXT, tags TEXT, anchors_json TEXT
);
CREATE VIRTUAL TABLE entries_fts USING fts5(
  title, body, tags, content=''
);
```

`memory_search(query, scope)` = fts5 bm25 over title+body+tags, filtered by scope and `status != superseded`, boosted by recency and type weight (gotcha > decision > insight > reference > task by default). returns `(path, short-id, title, first-paragraph excerpt, score)`. the agent reads the full file if it needs the detail.

index maintenance: incremental update on every `Memory.Write`; full rebuild at dream end and via `harness reindex`.

## 7. write protocol

every mutation goes through `Memory.Write(entries) → CommitSHA`. one logical operation = one commit:

- echo: `echo(saleskick): 3 entries from s-20260612-a4f2`
- dream: `dream: consolidated 14→9 entries, archived 3, promoted 1 to global` (body of commit message = the dream report)
- human edits: commit however you like

merge strategy across machines: the repo syncs via normal git remote. entries are one-file-each so conflicts are rare; MEMORY.md conflicts are resolved by regeneration (`harness reindex` after pull, never hand-merge).

## 8. budgets (config.toml)

```toml
[budgets]
index_tokens = 1500
inject_tokens = 3000
entry_first_para_words = 120

[staleness]
diff_threshold = 0.4
reference_reverify_days = 90

[dream]
max_cost_usd = 1.50
archive_after_unretrieved_dreams = 6

[sessions]
ttl_days = 30                 # raw jsonl transcripts self-destruct
distill_before_delete = true  # a session is never deleted unless an echo
                              # commit references it (or ttl is forced)
```

## 9. invariants (the contract)

1. the markdown tree is the only source of truth; the sqlite db and MEMORY.md are derived and reproducible.
2. every mutation is exactly one git commit through `Memory.Write`.
3. scope = path. no entry states its own scope.
4. speculation (`questions.md`) never merges into entries or documents without a dream or human explicitly creating/updating an entry.
5. injection content is immutable for the lifetime of a session.
6. archive, never delete. `archive/` is the only terminal state besides human `rm`. (applies to *distilled* memory only; raw transcripts are the ephemeral tier and self-destruct per `[sessions]` policy.)
7. `author: human` is load-bearing: machines don't overwrite humans.
8. raw is ephemeral, distilled is permanent. transcripts exist to be distilled, then vanish. only what was learned survives, in readable form.
9. zero telemetry. raqim phones home to nobody, ever. the only network calls are the llm providers you configured, carrying only what the active session needs.
