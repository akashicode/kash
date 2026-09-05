# Changelog

All notable changes to Kash are recorded here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and
Kash uses [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

A note on what "breaking" means here. Kash compiles a corpus into embedded
databases, so a change can break a *built corpus* without breaking the CLI. Any
release that changes chunk boundaries, chunk metadata or the extraction
vocabulary needs `kash build --rebuild` to take effect on an existing corpus —
those are called out under **Requires rebuild**.

## [Unreleased]

### Fixed

- **The generic reference words missed the commonest division of all.** The
  built-in list covered section, clause, article and part, so a query naming a
  chapter took no reference route at all — nor did paragraph, rule, schedule,
  annex or appendix, which any policy, statute or standard uses. Added, along
  with the section symbol, which never worked: a word boundary before a symbol
  needs a word character in front of it, so `§ 9` at the start of a query could
  not match. Deliberately still excluded are "item" and "step", which occur
  constantly in ordinary prose. Measured on a corpus that uses none of these
  words, the wider list added one tag across 895 chunks.

- **A scheme strongly used by one work was outvoted by a passing mention in
  another.** Sequence scoring averaged across documents, so a scripture
  numbering 101 of its own verses (0.84) and a commentary citing nine of them
  (0.49) produced 0.66, describing neither. The strongest document now decides:
  a numbering scheme belongs to the work that uses it, and another work quoting
  it in passing is not evidence against it.

- **Numbering that did not start at 1 was scored as though it started
  nowhere.** A scheme was rewarded only when its lowest value was 1, 2 or 3,
  which assumes every work is quoted whole. An anthology quoting ślokas 7 to 102
  begins at the beginning of what it quotes; that penalty alone sank a scheme
  carrying 41 headings and 40 distinct numbers, leaving its book with 8% of its
  chunks addressable against a companion volume's 50%. The test is now relative
  to the scheme's own range, so a run starting a short way in still counts while
  page numbers starting halfway through do not.

- **A numbering scheme used by only one document was discarded.** Detection
  required a scheme to appear in at least two documents before accepting it.
  That was safe on the 61-document corpus it was calibrated against, where any
  real scheme appeared in several, and wrong on a small mixed-genre one, where
  each work brings its own convention. A scripture numbering 45 distinct
  passages as `97)` — 51 occurrences, monotonicity 0.77, comfortably above the
  acceptance score — was rejected because the two commentaries beside it did not
  use that form, leaving 46 of its 112 numbered passages unreachable by number.
  Document spread still counts as a term in the score; it no longer vetoes.

- **One numbered heading suppressed every other section's numbering in the same
  chunk.** A section whose heading already numbers it should not also take
  numbers from its own body, or an ordinary list becomes verse numbering. That
  rule was applied across the whole chunk rather than per section, so a listing
  of `31)` and `32)` packed beside a `Verse 33` heading lost both of its
  numbers. It was invisible while a bare listing wrote a different metadata key
  from the heading, and became a real loss as soon as both named the same key.

- **A reference could be indexed and still unreachable.** The exact-reference
  lookup has two sides that name the key independently: the chunker names it
  from the pattern that matched the document, the query router from the pattern
  that matched the query. Those are different texts, so the names need not agree
  even when they mean the same passage — and nothing enforced that they would.
  A work numbering its passages both as `Verse 51` in headings and `97)` in a
  listing files the first under `verse` and the second under `section`; on the
  corpus this was found on, 51 of 112 numbered passages were reachable only as
  a section, and asking for the verse fell through to pure similarity, which
  returned a back cover ahead of the passage. The route now falls back to
  matching the number under any reference key when the named key holds nothing.
  It can only add hits where there were none, so it never displaces a correct
  match. Measured against an already-built index, with no rebuild: 61 of 112
  reachable becomes 112 of 112.

- **Reference patterns kept punctuation they captured by accident.** They end in
  `(\d[\d.]*)` so they can capture `4.2`, which means they also capture the full
  stop ending *"…in accordance with clause 22."* Stored as `22.`, that reference
  matched no query for 22. Values are normalised when written and when compared,
  so an index built before this resolves too.

- **The one numbering scheme that needed naming was the one that could not be
  named.** Every scheme is named by the word beside its number, and the model is
  asked to supply a metadata key for each. The bare `48)` form has no word — its
  pattern is punctuation and a digit class — so the match that assigns the
  model's answer, a substring test against the pattern, could never fire for it,
  and it stayed under the generic `section` key. That is why a verse could be
  cited as `Section 96` and why the graph named `Section N` entities. The match
  is now by scheme identity, and the model is told what the wordless label
  means so it can answer with the corpus's own word — `clause` in a contract,
  `verse` here.

- **Structural references away from the start of a chunk were never indexed.**
  Reference patterns are matched against whole multi-line chunk bodies, but were
  compiled without `(?m)`, so a leading `^` meant start-of-*body* rather than
  start-of-line. A pattern like `^\s*(\d{1,4})\)` therefore tagged a marker only
  when the chunk happened to begin with it, and every later marker in the same
  chunk was dropped — present in the text, and unaddressable by number. On the
  corpus this was found on, 22 of 112 numbered passages were missing their
  reference; queries naming them took the exact-reference route to an empty
  result and fell back to similarity, returning the book's introduction instead
  of the passage asked for. Coverage on that corpus goes from 90/112 to 112/112.

  This affected any corpus whose markers sit mid-chunk — `Section 4.2` in a
  spec, `Clause 7` in a contract, `48)` in a numbered list — not only verse
  numbering.

- **The chunker and the retrieval layer compiled the same patterns
  differently.** Queries are single-line, so the missing flag was harmless
  there and destructive at build time; the two sides could disagree about what
  a pattern matches. Both now compile through `chunker.CompileRefPattern`.

- **Headings now take precedence over the body, per reference key.** A heading
  names what a chunk *is*, so an ordinary numbered list in the body no longer
  contributes bogus reference numbers to a chunk a heading already numbered.
  The body is still scanned for every key no heading answered, which is what
  keeps a run of numbered passages addressable by each of them.

- **Chunks repeated their own overlap text.** Consecutive pieces of a section
  share an overlap tail so a passage split across a chunk boundary reads whole
  in both. When two such pieces were packed into the *same* chunk there was no
  boundary to bridge, and they were joined verbatim — so a sentence appearing
  once in the source appeared twice in the retrieved passage. A window that
  consisted of nothing but the carried tail was also emitted as a piece of its
  own, though the function had always claimed in a comment to drop it. Affected
  chunks across the two books measured: 37 → 4.

- **Chunks ran past the size they were budgeted against.** The body was packed
  to the full chunk size and the citation header was prefixed to it afterwards,
  so every chunk that filled its buffer overshot by the length of its header.
  Packing now uses the same content budget the pieces were cut to.

- **A citation header could be longer than the passage it located.** A heading
  is whatever follows the hashes, so a document that numbers its passages by
  putting the passage in the heading produced a breadcrumb segment hundreds of
  characters long — unquotable, and prefixed to every chunk of that section.
  Segments are now capped for display; chunk metadata keeps the full heading.
  The ratio that was meant to bound this clamped the *deduction* rather than the
  header, which inverted the guarantee it was written for.

- **Entity summaries were shown however weakly they matched.** The entity query
  is top-K with no cutoff, so it returned its full quota whenever the corpus held
  that many entities. On a question the corpus could not answer, the context
  block opened by orienting the reader around whatever was nearest in embedding
  space. The relevance floor already applied to graph seeding and provenance now
  applies to what is shown, and is a named constant rather than a literal
  repeated at three call sites.

### Added

- **`kash build` reports reference keys that tagged nothing.** Profiles outlive
  the corpus they were derived from, so a profile carries patterns for
  structures the current documents may not contain. Such a key is dead weight —
  queries naming it silently fall back to similarity — and the build now says
  so instead of leaving it invisible.

### Known limitation

A chunk can still exceed the configured size when a single source line is longer
than the budget: splitting cuts on line boundaries, so a paragraph written as one
unbroken line is indivisible and is emitted whole. On the corpus measured this
accounts for nearly all remaining oversized chunks — 422 of 434 — and is
unchanged by this release.

### Requires rebuild

Run both flags on an existing corpus:

```bash
kash build --rebuild --refresh-profile
```

Both are needed, and the build enforces it. Detection now proposes different
reference patterns, which changes the domain signature, so `kash build` refuses
an already-indexed corpus until it is re-chunked. `--rebuild` alone is not
enough: it leaves `data/domain.profile.json` in place, and the profile is where
detection's output lives, so without `--refresh-profile` the old patterns
survive and nothing this release fixes in detection takes effect.

What each flag recovers:

- `--refresh-profile` re-runs detection, which is what finds a numbering scheme
  only one document uses, stops a passing citation outvoting the work that owns
  the scheme, and lets the model name a wordless scheme in the corpus's own
  word. It costs one model call.
- `--rebuild` re-chunks, which is what applies per-section heading precedence,
  removes duplicated overlap text, keeps chunks inside their size, and clears
  the punctuation reference values used to keep. It re-embeds and re-extracts,
  so it costs provider calls in proportion to the corpus.

Serving an un-rebuilt corpus still works — there is no signature check at serve
time — and one repair lands there without any rebuild at all: the exact-
reference route's fallback to any key matches an index you already have.

The manifest records a `chunker_rules_version` (now 5) alongside the domain
signature, so a corpus built by an older binary is recognised and reported.

## [2.0.0] - 2026-09-04

### Added

- **Corpus profile.** `kash build` now measures your documents and writes
  `data/domain.profile.json`: the diacritic mode, structural numbering patterns,
  title stopwords, stem-vowel folding, and — via one model call — the extraction
  vocabulary, priorities and honorifics. Configuration is layered
  `defaults < profile < agent.yaml`, so `agent.yaml` is an override you may
  never need to open.
- **`kash verify`** audits the provenance chain. It walks the graph, fetches
  each fact’s chunk from the vector store, and reports how much of the graph
  can be shown to a reader in the passage it came from — with examples of
  what cannot. On a real corpus the answer is a number, not a hope.
- **`kash profile`** shows what was measured, the evidence behind each decision,
  and which fields `agent.yaml` overrides. `--dry-run` re-derives without
  writing, `--no-llm` measures only, `--refresh` regenerates.
- **Hybrid retrieval.** A pure-Go BM25 index (`data/lexical.idx`) is fused with
  vector search by Reciprocal Rank Fusion, alongside an exact-reference route
  for queries naming a section or verse number.
- **Structure-aware chunking.** Documents are split on their heading structure
  with citation breadcrumbs, and chunks carry structural metadata (book,
  heading path, reference numbers, content type).
- **Semantic graph retrieval.** Entity and relationship descriptions are
  embedded into their own collections, so the knowledge graph can be reached by
  meaning rather than only by exact token overlap. Entity hits seed graph
  traversal.
- **Chunk-level provenance, end to end.** Every graph fact records the chunk it
  was extracted from, and entities carry the passages their facts came from.
  At query time all three graph routes — traversal, the entity vector store
  and the relationship vector store — return chunk ids, and those passages are
  fetched and fused with vector hits. A semantic match on a generated
  description therefore arrives with the source text behind it, rather than a
  synthesis the reader cannot check.
- **Query decomposition** splits a question into specific entities and broad
  concepts to seed graph search, with a short-query bypass and an LRU cache so
  most queries cost no extra model call.
- **Version stamping.** Local builds now report a real version, commit and build
  date; the manifest and profile record the binary that produced them.
- **Reasoning effort.** Reasoning models can be driven at `low`, `medium` or
  `high` through `LLM_REASONING_EFFORT`, `llm.reasoning_effort` in
  `~/.kash/config.yaml`, or `runtime.llm.reasoning_effort` in `agent.yaml`
  — last wins. It applies to build-time extraction, entity adjudication and
  profiling as well as to serving, and a chat request may override it per call.
  Unset means the parameter is not sent at all, so non-reasoning models are
  unaffected.
- **Relationship weight as a corpus-time quality signal.** Each canonical triple
  now accumulates a co-occurrence count — the number of distinct chunks the same
  (subject, predicate, object) triple was extracted from. The count is stored in
  the relationship vector-store metadata at build time (`weight` field on
  `RelationshipDoc`) and used at retrieval time to apply a `log1p(weight)` boost
  to graph fact scores in `rankFactsByContext`, so facts attested in many chapters
  surface above those extracted from a single passage. Corpora built before this
  change degrade gracefully: a missing `weight` key is treated as `w = 1` (uniform
  factor, relative order unchanged). A rebuild is not required but will stamp
  weights on an existing corpus. The boost reads the weight of every candidate
  fact from the graph index, not only of the handful the relationship vector
  store returns, so it reaches the whole result set rather than its first rows.
- **Gleaning (iterative extraction).** Extraction now performs iterative follow-up
  passes per passage batch to recover facts the model missed due to token limits
  or lost attention on dense passages. Each gleaning pass appends the model's
  previous extraction as an assistant message and sends a continuation prompt
  requesting any explicitly stated facts not yet captured, deduplicating newly
  returned triples against existing extractions. Configured via `extraction.glean_rounds`
  (default: 1 in `DomainConfig`, layered via `defaults < profile < agent.yaml`),
  with early exit whenever a pass yields no new unique triples or an empty array.
  Setting `glean_rounds: 0` disables gleaning for single-pass extraction.
  Budget for it: a round that finds something costs a second model call for that
  batch, so a dense corpus approaches double the extraction calls of a single
  pass. The early exit keeps that cost proportional to what is actually
  recovered.

### Fixed

- **BM25 and entity resolution were dead in every Docker deployment.** The
  scaffolded Dockerfile copied three paths while `kash serve` reads five, so
  `lexical.idx` and `entity_aliases.json` never reached the image and hybrid
  retrieval silently degraded to vector-only. It now copies `data/` wholesale,
  and the build warns when an existing project's Dockerfile is affected.
- **`retrieval.top_k` never applied.** Every caller passed a hardcoded default,
  so the configured value was unreachable on the chat, A2A and dashboard paths.
- **`kash build` deleted every comment in `agent.yaml`** by round-tripping it
  through a map to update the MCP description. Nothing writes `agent.yaml` now.
- **Corrupt PDF text was indexed silently.** A PDF whose font subsets carry no
  usable `ToUnicode` CMap yields glyph indices — valid UTF-8 that decodes as a
  substitution cipher. A text-quality gate now rejects it instead of embedding
  it.
- **Truncated extraction responses looked like "no facts".** A batch that
  returned unusable output was checkpointed as complete and never revisited.
- **A reference pattern without a capture group crashed** the build or a query
  with an index-out-of-range panic. Patterns are now validated on compile.
- **Build artifacts were walked as source documents**, so every build reported
  its own manifest and embedded-store files as "not indexed".
- **The lexical index did not record its diacritic mode.** A corpus built with
  one mode and served with another tokenised queries differently from its own
  index and returned nothing, with no error. The index now pins its own mode.
- **Graph term matching measured length in bytes**, discarding short Latin terms
  like "om" while admitting any single Devanagari character.
- **Derived honorifics lost their trailing space**, which is what separates a
  title from the start of a word. Honorifics are stripped with a plain prefix
  cut, so a mined `"śrī "` echoed back as `"śrī"` would have eaten the front of
  every entity beginning with those letters. Values filtered against a supplied
  list now come back in the list's own spelling rather than the model's.
- **Release notes never came from the CHANGELOG.** The release workflow built a
  dynamic regex as `"^## \[" ver "\]"`, but awk resolves `\[` inside a
  string literal to a plain `[`, leaving the character class `^## [1.0.0]` —
  which matches no heading. An empty section falls back to the commit list by
  design, so a release would have shipped commit subjects while appearing to
  honour the CHANGELOG. The heading is now matched as a plain string.
- Reranker responses are bounds-checked; a provider returning an out-of-range
  index previously panicked the request. `/health` now reports reranker status
  from the real gate rather than a partial one.
- **A build could hang forever on one provider call.** Every HTTP client was a
  zero-value `http.Client` and the build passed a context with no deadline, so
  a provider that accepted a request and then stalled — or an intermediary that
  dropped the connection without a RST — blocked the process indefinitely. The
  retry loop was no defence: a call that never returns never returns an error
  either. Provider calls are now bounded by a response-header timeout that
  scales with reasoning effort, and extraction prints a live batch counter so a
  stall no longer looks identical to slow progress.
- **Every rerank request carried the whole candidate pool.** The pool is sized
  for fusion, which is local and free, so a default `top_k` shipped 200 chunks
  to a paid API billed per hundred, and a configured `top_k: 50` would have
  shipped 1000 and likely been rejected — silently, since a failed rerank falls
  back to cosine order. The reranker now sees the first 100 candidates and the
  rest keep their similarity order behind them.
- **Facts with unknown provenance borrowed the first chunk in their batch.**
  When no passage in a batch showed evidence for a fact, attribution fell back
  to the first one, which fabricated a citation: the fact printed as
  "[passage 1]" and took the chunk-level ranking boost, both on text that does
  not support it. Unknown provenance now stays unknown, and the fact keeps its
  document citation.
- **A fact’s passage citation was never checked.** The extractor reports which
  passage it used and that report became the chunk id unconditionally, so a
  misreported index printed a passage citation on text that does not support
  the fact and took the chunk-level ranking boost with it. An evidence check
  existed but ran only when the model declined to answer, which is backwards.
  The claim is now evidence rather than authority: preferred when the passage
  it names mentions the fact, and the batch searched otherwise. Matching folds
  the way the corpus does, so a transliterated name still matches its source.
- **Generated entity summaries were indistinguishable from quoted source.**
  Descriptions written by a model at build time appeared under the same
  heading style as retrieved passages, so an answer could rest on a summary
  and cite a document that never contains those words. They are now labelled
  as generated orienting context, and the prompt asks for every claim to be
  grounded in a numbered passage or a graph fact.
- **Internal chunk IDs reached the prompt as pseudo-citations.** A graph fact
  whose supporting chunk was not retrieved printed "chunk: tantra_md_312" —
  a string that looks like a reference to a model instructed to cite inline,
  and that no reader can look up. Such a fact now cites its document alone.
- **Entity and relationship search failed outright on a small corpus.** Both
  asked for five results unconditionally, and the vector store errors when
  that exceeds the collection size instead of returning what it has. Both
  failures are non-fatal, so the dense graph routes went quiet with only a
  warning. Result counts are now clamped to the collection.
- **The citation instruction under-used what retrieval provides.** It asked
  only for a filename, while every passage carries a number and a structural
  location (book, chapter, verse). It now asks for both.
- **Triple extraction was capped at two facts per chunk.** The prompt asked for
  "5-20 triples" per batch of ten passages, which rationed dense passages
  regardless of what they stated — measured at 15.3 triples per batch against
  that ceiling of 20. The budget is now per passage, so a passage contributes
  what it actually contains.

### Changed

- `kash init` scaffolds an override file rather than a questionnaire:
  `agent.yaml` drops from ~121 lines of domain guesswork to ~78, with the
  derived settings documented as optional overrides.
- Retrieval candidate depth scales with the request instead of a fixed ceiling
  of 40, which previously capped recall for the entire system.
- Result diversity is capped per *work* rather than per file, so several
  editions of one text no longer compete as separate books — and a question
  genuinely concentrated in one text can be answered from it.

### Requires rebuild

Run `kash build --rebuild` on an existing corpus to pick up the new chunk
boundaries, structural metadata and BM25 index. The build refuses to proceed if
structural rules changed under an already-indexed corpus, rather than leaving
the vector and lexical indexes disagreeing about the same chunk.

Existing Docker projects should replace the per-file `COPY` lines in their
Dockerfile with `COPY data/ /app/data/`; `kash build` warns when this applies.

Several graph changes are softer. A rebuild re-extracts under the per-passage
triple budget and with gleaning, writes entity-level chunk provenance, and
stamps relationship weights. None of it is required — an existing graph stays
correct, just sparser and less traceable, and absent provenance or weights read
as unknown rather than failing.

## [1.0.0]

- Entity resolution: spelling variants of the same entity are merged so graph
  chains connect across them, with deterministic rules first and optional model
  adjudication for the undecidable cases.
- Domain-configurable extraction and resolution via `agent.yaml`.
- Query-time graph traversal, query rewriting, and a dashboard UI.
- Incremental, resumable builds tracked by a build manifest.

## [0.1.0]

- Initial release: `kash init`, `kash build`, `kash serve`; chunking, embedding
  into chromem-go, LLM triple extraction into cayley, and the REST, MCP and A2A
  interfaces.

[Unreleased]: https://github.com/akashicode/kash/compare/v2.0.0...HEAD
[2.0.0]: https://github.com/akashicode/kash/compare/v1.0.0...v2.0.0
[1.0.0]: https://github.com/akashicode/kash/compare/v0.1.0...v1.0.0
[0.1.0]: https://github.com/akashicode/kash/releases/tag/v0.1.0
