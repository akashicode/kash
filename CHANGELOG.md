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

### Added

- **Corpus profile.** `kash build` now measures your documents and writes
  `data/domain.profile.json`: the diacritic mode, structural numbering patterns,
  title stopwords, stem-vowel folding, and — via one model call — the extraction
  vocabulary, priorities and honorifics. Configuration is layered
  `defaults < profile < agent.yaml`, so `agent.yaml` is an override you may
  never need to open.
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
- **Chunk-level provenance.** Graph facts record the chunk they came from, not
  just the document, so a fact can cite the passage supporting it and graph hits
  can fuse with vector hits by chunk.
- **Query decomposition** splits a question into specific entities and broad
  concepts to seed graph search, with a short-query bypass and an LRU cache so
  most queries cost no extra model call.
- **Version stamping.** Local builds now report a real version, commit and build
  date; the manifest and profile record the binary that produced them.

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
- Reranker responses are bounds-checked; a provider returning an out-of-range
  index previously panicked the request. `/health` now reports reranker status
  from the real gate rather than a partial one.

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

[Unreleased]: https://github.com/akashicode/kash/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/akashicode/kash/compare/v0.1.0...v1.0.0
[0.1.0]: https://github.com/akashicode/kash/releases/tag/v0.1.0
