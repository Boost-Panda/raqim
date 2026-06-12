# raqim

a coding harness that remembers what matters and forgets everything else.

raqim takes its name from al-raqīm (الرقيم) in surah al-kahf: the inscription
of the sleepers of the cave, who slept for centuries and woke with their
knowledge intact, a written record preserving what time would otherwise have
erased. the root r-q-m means to write, to inscribe. that is the whole system:
sessions sleep, the inscription remains. one founding conviction carries over
from its predecessor: "trust us" is not an architecture.

## principles

**raw is ephemeral, distilled is permanent.**
session transcripts self-destruct on a ttl. before they go, an echo distills
what was learned. only the lesson survives. the session is gone, the
inscription remains. that's the raqim.

**verifiable over promised.**
memory is markdown in a git repo. you can read your agent's mind in a text
editor. every mutation is one commit. the nightly dream cycle that consolidates
memory produces a single reviewable, revertible commit with the dream report as
its message. don't take the dream's word for it. read the diff.

**no hidden state.**
the markdown tree is the only source of truth. the search index and the memory
index are derived caches, reproducible byte-identically with `raqim reindex`.
anything that can't be rebuilt from the tree doesn't exist.

**data sovereignty.**
per-project model policy. mark a project `local-only` and its echo and dream
runs never leave your machine. memory injection is need-to-know: a provider
sees one project's index, never your world.

**zero telemetry.**
raqim phones home to nobody. not opt-out. absent. the only network calls are
the llm providers you configured, carrying only what the active session needs.

**machines don't overwrite humans.**
entries you've corrected are marked `author: human`. dreams may question them,
never rewrite them.

**no account, no daemon, no friction.**
one static binary. works offline against local models. install to first
session in under a minute.

**honest about rough edges.**
dreams can confabulate, so speculation is quarantined in questions.md and never
merges into facts on its own. staleness is flagged, not hidden. when raqim
isn't sure, it says so in the index.
