---
status: accepted
date: '2026-11-08'
supersedes: []
tags:
  - adr
  - memory
  - search
  - embeddings
  - datastore
---

# 0040 — Semantic search over the archive, and the keyword path beneath it

## Context

[ADR 0037](./0037-memory-tiers-load-timing-and-management.md) decision 2 made
long-term memory a **searched** tier and decision 3 gave it a **bounded, ranked**
read contract. [NAVI-578](https://huly.flintwillow.software) shipped that
surface: `ltm_write`, `ltm_search`, `ltm_read`, `ltm_prune`, with the tier
boundary and the bound enforced.

What shipped is **keyword search only**, and ADR 0037 open question 3 left the
semantic half undecided:

> **The embedder.** The doc requires one supplied by the navi author at
> construction; whether a navi without an embedder gets keyword-only search or no
> long-term tier at all is undecided.

Three things are true in the code today, and they are the context this ADR
resolves.

**First, the embedder already exists and nothing uses it.**
`internal/llm/embedder.go` defines an `Embedder` interface
(`Embed`/`Dimensions`/`Name`) and a working `OllamaEmbedder` over Ollama's
`/api/embeddings`, defaulting to `nomic-embed-text` (768-dim). It is dead code:
no caller, no test, no wiring. So open question 3's "what is that interface" is
already answered in the code — what is missing is the *plumbing*, not the
interface.

**Second, the vector index already exists in the schema and nothing writes it.**
Migration 5 created:

```sql
CREATE TABLE IF NOT EXISTS memory_embeddings (
    memory_id TEXT PRIMARY KEY REFERENCES memories(id) ON DELETE CASCADE,
    embedding BLOB NOT NULL,
    dims INTEGER NOT NULL,
    created_at DATETIME NOT NULL
)
```

with the design note *"embeddings stored as BLOBs in the same DB, brute-force
cosine in Go (sqlite-vec/vec0 is a C extension, unshippable under
CGO_ENABLED=0)."* Nothing writes it and nothing reads it.

**Third, the keyword path does not use the index built for it.** Migration 4
created `memories_fts` — an FTS5 virtual table with `content='memories'`,
`tokenize='unicode61'`, and three sync triggers (`memories_ai`, `memories_ad`,
`memories_au`) plus a `rebuild`. It is maintained on every write and **never
queried**. `MemoryStore.Recall` instead does an in-Go substring scan:

```go
needle := strings.ToLower(q.Query)
for rows.Next() {
    ...
    if needle != "" && !strings.Contains(strings.ToLower(rec.Content), needle) {
        continue
    }
```

with the comment *"content substring and tags are applied in Go so the matching
(case-insensitive, every tag required) is exactly the contract's, independent of
SQLite's collation or FTS tokenizer."* That reasoning was correct for a
**contract-pure** `datastore` package, and it is why the scan is there. But it
means every archive search is O(n) over the whole archive in Go, and the FTS5
index is dead weight: it costs write amplification on every `Remember` and buys
nothing.

The archive is searched rather than injected, so a full scan is *permitted* by
the tier's own trade (ADR 0037 decision 6). But NAVI-578's own benchmark found
`thoughts_search` over 1000 thoughts at ~1.3 ms, and the archive has no such
measurement — the difference is that the thought queue is bounded by drain while
the archive grows without limit by design (ADR 0037 decision 6 says long-term
"may grow"). A search that is fine at 100 entries is not obviously fine at
100,000.

This ADR settles open question 3 and the keyword-path question it raises. It does
**not** re-open the tier boundary, the bound, or the retention model.

## Decision

1. **The embedder interface is already the right one; it moves to a public
   package and the navi author supplies it.** `llm.Embedder` is the interface
   ADR 0037 called for — one method for text, plus `Dimensions` and `Name`. It
   is not a new abstraction. It moves from `internal/llm` to a package an
   out-of-repo navi can import, because ADR 0037 decision 3 says the embedder is
   *"supplied by the navi author at construction"* and an author cannot supply a
   type from an `internal` package. `OllamaEmbedder` remains the default
   implementation and moves with it.

   The move is deliberate rather than lazy: the interface is a **contract** the
   way `datastore.Backend` and `model.Provider` are, and contracts live where
   authors can reach them.

2. **A navi with no embedder gets keyword search — the archive is never
   disabled.** This settles open question 3's fork. "No long-term tier at all"
   was the alternative and it is the wrong shape: the tier's value is *parking a
   conclusion you cannot act on now*, and that value does not depend on the
   search being semantic. A navi that can write and keyword-search an archive is
   strictly better off than one with no archive, and requiring an embedder would
   make the tier's availability depend on a model being installed — a
   deployment accident deciding a capability. So: **keyword always, semantic
   additionally when an embedder is declared.** The tool surface is identical in
   both cases; only the ranking differs.

3. **Keyword search moves onto FTS5, and the `datastore` contract does not
   change.** `MemoryStore.Recall` uses `memories_fts` for `q.Query` instead of
   the in-Go scan. The index already exists, is already maintained, and is
   already synced — this is a query-side change with no migration.

   The reason this is safe now when it was not before is that the *contract*
   does not move: `RecallQuery.Query` remains **"a literal substring match on
   Content unless a backend supports more"**, and the FTS5 path is a backend's
   own optimisation of that promise, not a new promise. The difference a
   tokenizer makes is real and is **documented rather than hidden**: `unicode61`
   matches on token prefixes and word boundaries, so a query for `"config"` will
   match `"configuration"` (which substring matching would not), and a query for
   a mid-word fragment like `"onfig"` will not match `"configuration"` (which
   substring matching would). That is a **behaviour change to keyword search**,
   it is the point of using an index, and it is recorded here rather than
   discovered later.

   A backend that cannot support FTS keeps the scan; the fallback is the
   contract's literal reading, so `Recall` is correct either way.

4. **Embeddings are written on write, in the same transaction as the entry.**
   Not on read, not lazily, not by a backfill job. The reasons, in order:

   - **Staleness is the failure mode that matters.** An entry edited after being
     embedded has a vector describing text that no longer exists. Embedding on
     write makes the vector and the content change together, so the invariant
     "an embedded entry's vector describes its current content" holds by
     construction.
   - **The write is already a deliberate act.** ADR 0037 decision 7 makes
     retention deliberate; the same logic applies to an entry's cost. A navi
     writing an archive entry is doing something slow and rare on purpose.
   - **Embedding on read puts a network call on the search path**, which makes
     `ltm_search` latency depend on a model server being up — turning a local
     index lookup into a distributed one. Rejected.

   **The cost is stated plainly:** a navi with an embedder declared cannot write
   an archive entry while the embedder is unreachable. That is the same trade
   ADR 0039 decision 1 made for the short-term bound (a write that cannot be
   honoured is refused rather than silently degraded), and it is consistent with
   it. The failure is loud, not silent.

5. **An entry written before an embedder was declared is keyword-searchable and
   not semantic-searchable, and that is not an error.** No backfill runs
   automatically — a background job that embeds the archive is exactly the
   "runtime pass that decides what matters without the reasoning the thought
   just did" that ADR 0037 decision 9 rejects. The archive is heterogeneous for
   the lifetime of a navi that added an embedder late, and the search path
   simply ranks the entries it has vectors for by similarity and the rest by the
   keyword ranking. A **explicit** backfill is available as a tool for an
   operator who wants it, and it is the same deliberate-act shape as `ltm_prune`.

6. **Hybrid ranking is reciprocal rank fusion, not a weighted score.** When both
   paths produce candidates, the two ranked lists are combined by **RRF**
   (`score = Σ 1/(k + rank)`, `k = 60`), which needs no tuning and no
   normalisation. The alternative — blending a cosine similarity with an
   importance/recency score — requires putting a distance and a rank on one
   scale, which is a tuning knob that has no correct value and would need
   revisiting per embedder. RRF is rank-based, so it is indifferent to the two
   scorers' units, and it degrades gracefully: with one list non-empty it is
   that list's order.

   The archive's existing ordering (**importance, then recency**) is preserved as
   the **tiebreak**, so an entry that RRF cannot separate still lands where it
   would have before this ADR. Search remains **bounded and ranked** exactly as
   ADR 0037 decision 3 requires — this ADR changes *how* relevance is computed,
   not the contract that the result is bounded with a reported total.

7. **The vector store is brute-force cosine in Go, over BLOBs in the same
   database.** This is migration 5's own design note, confirmed rather than
   revisited: `sqlite-vec`/`vec0` is a C extension and is unshippable under
   `CGO_ENABLED=0`, which the project's SQLite backend requires (`modernc.org/sqlite`
   is the pure-Go driver). Brute force is **linear in the archive size**, which is
   the same shape as the scan being removed — so this ADR does not make search
   asymptotically worse, and the FTS5 change in decision 3 is what buys the
   headroom. A vector index that scales better is a later change to this
   decision, not a precondition for it.

8. **Embedding failures never corrupt the archive.** An entry that cannot be
   embedded is either refused (decision 4, on write) or stored without a vector
   (decision 5, pre-existing entries). A **vector whose `dims` does not match the
   active embedder is ignored, not compared** — a navi that changed embedding
   models has two populations of vectors, and cosine between them is meaningless.
   Ignoring a stale-dimension vector degrades that entry to keyword-only, which
   is the same outcome as having no vector, and is the honest one.

9. **The tier boundary is unchanged, and semantic search does not cross it.**
   `ltm_search` searches the archive. Short-term memory is still always-injected
   and still not searchable, and no part of this ADR puts a vector on the run
   path. The embedder is called on **write**, never on injection.

## Consequences

- **The archive becomes semantically searchable without becoming mandatory.**
  A navi with no embedder loses nothing it had; a navi with one gains recall it
  could not get from substrings. The tier's availability is decoupled from the
  deployment.
- **Keyword search changes behaviour**, and this is the sharpest edge of the
  ADR. Moving to FTS5 means prefix/word-boundary matching replaces substring
  matching: `"config"` finds `"configuration"`, `"onfig"` does not. That is
  better for a knowledge archive (a navi searching for a topic is not searching
  for a fragment) and worse for anyone who relied on substring semantics. The
  `ltm_search` tool description and the `core_memory` skill must state which
  matching applies, so the behaviour is legible at the call site.
- **FTS5 stops being write-only.** The index currently costs write amplification
  for no benefit; after this it is the keyword path. This is a strict
  improvement to a pre-existing inefficiency.
- **A declared embedder becomes a write-path dependency.** An archive write
  fails if the embedder is unreachable. This is deliberate (decision 4) but it
  is a real coupling: a navi whose Ollama host is down cannot park a conclusion.
  The mitigation is that the error is loud and names the embedder.
- **Two vector populations can coexist after a model change**, and the old ones
  are silently ignored (decision 8). A navi that changes embedder models gets a
  degraded archive until the entries are re-embedded, and nothing tells it
  except that recall got worse. An explicit backfill (decision 5) is the remedy
  and it is manual.
- **Ranking is no longer explainable by a single number.** RRF produces a score
  that is a sum of reciprocal ranks, which is not meaningful on its own — a
  caller cannot say "this scored 0.8". The envelope reports *which paths matched*
  per entry instead, so a result is explicable by provenance rather than by
  score.
- **The `datastore` contract is unchanged**, which is what keeps an out-of-repo
  backend viable. No new method, no new type, no migration.

## Alternatives considered

- **No long-term tier without an embedder.** Rejected: makes a capability depend
  on a deployment accident, and discards the archive's parking value, which does
  not depend on semantic recall.
- **Embed on read.** Rejected: puts a network call on the search path and makes
  `ltm_search` fail when the model server is down.
- **Backfill the archive automatically when an embedder is declared.** Rejected:
  it is a runtime pass deciding what to keep without the reasoning that produced
  the entries — the thing ADR 0037 decision 9 explicitly rejects.
- **Weighted-score hybrid ranking.** Rejected: needs a tuning constant with no
  correct value, and a normalisation between a distance and a rank that changes
  per embedder. RRF is rank-based and needs neither.
- **`sqlite-vec` / a real vector index now.** Rejected for this change: a C
  extension, unshippable under `CGO_ENABLED=0`. Recorded as a later change, not a
  precondition.
- **Leave the keyword scan in Go and only add vectors.** Rejected: it leaves an
  O(n) scan *and* adds an O(n) cosine pass, so hybrid search would be slower than
  either. Moving the keyword path to FTS5 first is what makes the addition
  affordable.

## Open questions

1. **The RRF constant.** `k = 60` is the value from the original RRF paper and
   is used here as a default rather than a tuned one. Whether it should be a knob
   is undecided; it is deliberately not one yet, because a knob with no evidence
   for its value is worse than a stated default.
2. **Whether the embedder belongs on `model.Provider`.** ADR 0034 made the model
   registry describe chat models with routing and reasoning options. Embeddings
   have different parameters (no routing, no reasoning, a dimension). Whether an
   embedder is a second kind of `model.Provider` or stays a separate
   `WithEmbedder` declaration is undecided.
3. **Batching.** `Embedder.Embed` takes one text. An explicit backfill over a
   large archive would be far cheaper batched, and Ollama's newer `/api/embed`
   takes an array. Whether the interface grows a batch method, and whether that
   is worth the interface churn, is undecided.
4. **Dimension changes.** Decision 8 ignores mismatched vectors, which degrades
   silently. Whether a model change should instead *invalidate* the old vectors
   (with a reported count) is undecided.

## Amendment — decision 3's prefix matching is explicit, not implicit

**Recorded during implementation (slice 1), and it corrects a claim this ADR
made.** Decision 3 says FTS5 `unicode61` "matches on token prefixes and word
boundaries, so a query for `config` will match `configuration`". That is
**not** what FTS5 does by default, and the first implementation proved it: FTS5
matches whole **tokens**, so a bare `config` matches *nothing* against
`configuration`. Prefix matching requires an explicit trailing `*` (`config*`).

The decision itself is unchanged — the promised behaviour is still the
delivered behaviour — but it is delivered by the implementation adding `*` to
each term rather than by the tokenizer doing it for free. The term is quoted
first and the `*` appended **outside** the quotes (`` `"config"*` ``), because
inside the quotes it would be a literal asterisk. Quoting is what keeps a
navi's ordinary prose from being parsed as FTS5 query syntax, and it is
load-bearing: a search for `the flag is --dry-run` is a parse error unquoted
(`-` is an operator) and a valid search quoted.

So the two halves of decision 3's behaviour change hold, and the second is the
one worth re-reading:

- `config` finds `configuration` — **because the implementation appends `*`**,
  not because the tokenizer is prefix-aware.
- `onfig` does not find `configuration` — this one the tokenizer does give,
  since a mid-word fragment is not a token.

A term that is *only* punctuation (`` `--dry-run` ``) quotes to a phrase that
matches nothing, which is deliberate: the tokenizer never produced that token,
and stripping punctuation to find a near-match would search for something the
caller did not write.

This amendment is recorded rather than applied to decision 3 because an
accepted ADR is not edited. It is not a supersession: the decision stands
whole, and what changed is the record's *explanation* of how it is achieved —
which is exactly the thing a later reader would otherwise get wrong.
