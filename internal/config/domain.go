package config

import (
	"os"

	"gopkg.in/yaml.v3"
)

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

// DomainConfig holds the corpus-specific knobs that make Kash work on any
// subject matter. Everything here has a generic default; a specialised corpus
// (Sanskrit texts, aerospace engineering, case law) overrides what it needs.
type DomainConfig struct {
	Extraction ExtractionConfig `yaml:"extraction"`
	Resolution ResolutionConfig `yaml:"resolution"`
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
		},
		Resolution: ResolutionConfig{
			Honorifics:           []string{"the ", "a ", "an ", "dr. ", "dr ", "prof. ", "prof ", "mr. ", "mrs. ", "ms. ", "sir "},
			FoldDiacritics:       DiacriticLatin,
			StripFinalVowel:      false,
			ProperNounPredicates: []string{"created by", "authored", "was written by", "designed by", "developed by"},
		},
	}
}

// LoadDomainConfig reads the extraction and resolution sections from an
// agent.yaml. Missing sections fall back to the generic defaults, so an old
// agent.yaml keeps working unchanged.
func LoadDomainConfig(path string) DomainConfig {
	cfg := DefaultDomainConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		return cfg
	}

	var parsed DomainConfig
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return cfg
	}

	if len(parsed.Extraction.Predicates) > 0 {
		cfg.Extraction.Predicates = parsed.Extraction.Predicates
	}
	if len(parsed.Extraction.Priorities) > 0 {
		cfg.Extraction.Priorities = parsed.Extraction.Priorities
	}
	if parsed.Resolution.Honorifics != nil {
		// An explicitly empty list is meaningful: strip no honorifics
		cfg.Resolution.Honorifics = parsed.Resolution.Honorifics
	}
	if parsed.Resolution.FoldDiacritics.Valid() {
		cfg.Resolution.FoldDiacritics = parsed.Resolution.FoldDiacritics
	}
	if len(parsed.Resolution.ProperNounPredicates) > 0 {
		cfg.Resolution.ProperNounPredicates = parsed.Resolution.ProperNounPredicates
	}
	cfg.Resolution.StripFinalVowel = parsed.Resolution.StripFinalVowel

	return cfg
}
