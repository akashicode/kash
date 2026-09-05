package config

// DiacriticMode selects which alphabets' diacritics are folded when grouping
// entity spelling variants.
type DiacriticMode string

const (
	// DiacriticNone disables diacritic folding entirely.
	DiacriticNone DiacriticMode = "none"
	// DiacriticLatin folds European diacritics (é, ü, ñ, å, ç...).
	DiacriticLatin DiacriticMode = "latin"
	// DiacriticIAST folds Sanskrit transliteration marks (ā, ṣ, ṛ, ṃ...).
	DiacriticIAST DiacriticMode = "iast"
	// DiacriticBoth folds Latin and IAST marks.
	DiacriticBoth DiacriticMode = "both"
)

// Valid reports whether the mode is a recognised value.
func (m DiacriticMode) Valid() bool {
	switch m {
	case DiacriticNone, DiacriticLatin, DiacriticIAST, DiacriticBoth:
		return true
	}
	return false
}

// RefPattern configures one corpus-specific numbered-item pattern used by the
// chunker (to tag metadata) and the retrieval layer (to boost exact hits).
// Each pattern must contain exactly one capture group for the number.
type RefPattern struct {
	// Pattern is a Go regular expression with exactly one capture group that
	// captures the number (e.g. "4.2" or "7").
	Pattern string `yaml:"pattern"`
	// MetaKey is the chunk metadata field written when the pattern matches
	// (e.g. "section", "clause", "verse", "dharana").
	MetaKey string `yaml:"meta_key"`
}

// ChunkerConfig controls structural reference recognition used by both the
// chunker (to tag chunk metadata) and the retrieval layer (to boost exact
// reference hits). Everything here has a generic default; a specialised corpus
// overrides what it needs in agent.yaml.
type ChunkerConfig struct {
	// RefPatterns are the corpus-specific numbered-item patterns.
	// Each entry must contain exactly one capture group (the number); an entry
	// with any other number of groups is rejected at compile time.
	// Every pattern is applied and its hits accumulate — matches are additive,
	// not first-wins, so two patterns capturing the same number produce one
	// deduplicated value rather than shadowing each other.
	// The meta_key is the metadata field written on the chunk and used by
	// the exact-reference retrieval route (e.g. "section", "clause", "verse").
	RefPatterns []RefPattern `yaml:"ref_patterns"`

	// TitleStopwords are words stripped from document titles before grouping
	// editions of the same work together (diversity capping in fusion).
	// Override for your domain: a Sanskrit corpus needs "tantra", "paddhati";
	// a legal corpus needs "amended", "schedule", "exhibit".
	TitleStopwords []string `yaml:"title_stopwords"`

	// StripTitleStemVowel removes a trailing a/i/u/o/e from title tokens
	// before comparing them, normalising transliteration variants like
	// "bhairava"/"bhairav". This is a Sanskrit convention: leave false for
	// every other domain.
	StripTitleStemVowel bool `yaml:"strip_title_stem_vowel"`
}

// DomainConfig holds the corpus-specific knobs that make Kash work on any
// subject matter. Everything here has a generic default; a specialised corpus
// (Sanskrit texts, aerospace engineering, case law) overrides what it needs.
type DomainConfig struct {
	Extraction        ExtractionConfig        `yaml:"extraction"`
	Resolution        ResolutionConfig        `yaml:"resolution"`
	Chunker           ChunkerConfig           `yaml:"chunker"`
	EntityDescription EntityDescriptionConfig `yaml:"entity_description"`
}

// EntityDescriptionConfig controls node description generation and embedding.
type EntityDescriptionConfig struct {
	// MinDegree is the minimum number of graph facts an entity must appear in
	// to have a description generated and embedded (default: 2).
	MinDegree int `yaml:"min_degree"`

	// MaxEntities is the maximum number of entity descriptions to embed
	// (default: 500, 0 = unlimited).
	MaxEntities int `yaml:"max_entities"`
}

// ExtractionConfig controls build-time knowledge graph extraction.
type ExtractionConfig struct {
	// Predicates is the closed vocabulary the extractor must choose from.
	// Constraining it is what stops the extractor inventing a new phrasing for
	// every fact. Facts that fit no predicate are dropped, so this list must
	// cover the relations your corpus actually contains.
	Predicates []string `yaml:"predicates"`
	// Priorities are the relation types the extractor should favour, most
	// important first. Free text, injected into the prompt.
	Priorities []string `yaml:"priorities"`
	// GleanRounds is the number of iterative follow-up extraction passes run
	// after the initial one. Each round shows the model its own previous output
	// and asks it to recover any explicitly stated facts it missed — useful for
	// dense passages where the model runs out of attention or output tokens.
	// 0 disables gleaning (one pass only). Default is 1.
	GleanRounds int `yaml:"glean_rounds"`
}

// ResolutionConfig controls entity resolution (`kash resolve-entities`).
type ResolutionConfig struct {
	// Honorifics are leading titles stripped when comparing entity names.
	// They never change which entity is meant, so clusters formed by removing
	// them are auto-approved.
	Honorifics []string `yaml:"honorifics"`
	// FoldDiacritics selects the alphabet(s) whose diacritics are folded when
	// generating merge candidates: none, latin, iast, or both.
	FoldDiacritics DiacriticMode `yaml:"fold_diacritics"`
	// StripFinalVowel folds a trailing stem vowel (Gorakhnath/Gorakhnatha).
	// This is a Sanskrit convention and is OFF by default: in most languages
	// dropping a final "a" changes the word (plasma/plasm, corona/coron).
	StripFinalVowel bool `yaml:"strip_final_vowel"`
	// ProperNounPredicates mark an entity as a person, work or product.
	// For proper nouns, diacritic-only differences are almost always spelling
	// variants, so those clusters can be auto-approved rather than reviewed.
	ProperNounPredicates []string `yaml:"proper_noun_predicates"`
}

// defaultTitleStopwords are words that identify an edition, format, or
// document-lifecycle stage rather than the work itself. Generic across
// languages and domains; domain-specific words belong in agent.yaml.
var defaultTitleStopwords = []string{
	// English articles / prepositions
	"the", "of", "and", "with", "by", "for", "in", "on", "at", "to",
	// Document structure markers
	"vol", "volume", "part", "section", "chapter",
	"appendix", "schedule", "exhibit", "annex", "amendment",
	// Document lifecycle / pipeline suffixes
	"final", "draft", "revised", "original", "ocr",
	// Language / format tags
	"english", "translation",
}

// defaultRefPatterns are generic numbered-item patterns that work for
// contracts, policies, RFCs, standards, and API docs. Corpus-specific
// patterns (verse/dharana for Sanskrit, CFR § for US regulations) should
// be set in agent.yaml.
var defaultRefPatterns = []RefPattern{
	// Named structural units: "Section 4.2", "Clause 7", "Article 12", "§ 3"
	{
		Pattern: `(?i)\b(?:section|clause|article|part|§)\s*(\d[\d.]*)`,
		MetaKey: "section",
	},
	// Bare decimal heading numbers: "4.2", "3.1.4" — requires at least X.Y
	// to avoid matching bare years (2024) or single item numbers (1, 2, 3).
	{
		Pattern: `(?i)(?:^|[\s#(\[])(\d{1,4}(?:\.\d+)+)\b`,
		MetaKey: "section",
	},
}

// DefaultDomainConfig returns settings that work on a general-purpose corpus
// with no domain assumptions.
func DefaultDomainConfig() DomainConfig {
	return DomainConfig{
		Extraction: ExtractionConfig{
			Predicates: []string{
				"is a type of", "is part of", "contains", "is defined as",
				"causes", "requires", "describes", "located in",
				"created by", "used for", "associated with", "preceded by",
			},
			Priorities: []string{
				"Relations between people and organisations (who created, led, or influenced what)",
				"Conceptual relations (X is a type of Y, X causes Y, X requires Y)",
				"Structural facts (X contains Y, X is part of Y)",
			},
			GleanRounds: 1,
		},
		Resolution: ResolutionConfig{
			Honorifics:           []string{"the ", "a ", "an ", "dr. ", "dr ", "prof. ", "prof ", "mr. ", "mrs. ", "ms. ", "sir "},
			FoldDiacritics:       DiacriticLatin,
			StripFinalVowel:      false,
			ProperNounPredicates: []string{"created by", "authored", "was written by", "designed by", "developed by"},
		},
		Chunker: ChunkerConfig{
			RefPatterns:         defaultRefPatterns,
			TitleStopwords:      defaultTitleStopwords,
			StripTitleStemVowel: false,
		},
		EntityDescription: EntityDescriptionConfig{
			MinDegree:   2,
			MaxEntities: 500,
		},
	}
}

// ResolveDomainConfig layers configuration: built-in defaults, then the
// generated corpus profile, then agent.yaml. Later layers win per field, and
// any layer may be absent.
//
// The returned LayerNotes say which layer supplied each overridden value. A
// three-layer merge is otherwise impossible to reason about from the outside —
// "why is fold_diacritics latin when my corpus is Sanskrit" needs an answer
// that does not require reading three files.
//
// Overrides replace a field wholesale; they never merge element-wise. Setting
// ref_patterns in agent.yaml replaces the detected patterns rather than adding
// to them.
func ResolveDomainConfig(profile *DomainOverlay, agentYAMLPath string) (DomainConfig, []LayerNote) {
	cfg := DefaultDomainConfig()
	var notes []LayerNote

	if profile != nil {
		profile.applyTo(&cfg, layerProfile, &notes)
	}
	parseAgentYAMLOverlay(agentYAMLPath).applyTo(&cfg, layerAgentYAML, &notes)

	return cfg, notes
}
