package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// DomainOverlay is a parse-only mirror of DomainConfig in which every field is
// nullable, so three states can be told apart: absent (nil), explicitly empty
// (pointer to an empty slice), and set.
//
// DomainConfig itself cannot express "absent" — a missing bool is false and a
// missing slice is nil, which is also what "explicitly empty" looks like. That
// is survivable with one config source and broken with two: a corpus profile
// detecting strip_final_vowel: true would be silently reset to false by any
// agent.yaml that simply omits the key, which after the init-template change is
// every agent.yaml.
//
// Tagged for both yaml (agent.yaml) and json (the generated profile) so one
// type serves both layers.
type DomainOverlay struct {
	Extraction struct {
		Predicates *[]string `yaml:"predicates" json:"predicates"`
		Priorities *[]string `yaml:"priorities" json:"priorities"`
	} `yaml:"extraction" json:"extraction"`

	Resolution struct {
		Honorifics           *[]string      `yaml:"honorifics" json:"honorifics"`
		FoldDiacritics       *DiacriticMode `yaml:"fold_diacritics" json:"fold_diacritics"`
		StripFinalVowel      *bool          `yaml:"strip_final_vowel" json:"strip_final_vowel"`
		ProperNounPredicates *[]string      `yaml:"proper_noun_predicates" json:"proper_noun_predicates"`
	} `yaml:"resolution" json:"resolution"`

	Chunker struct {
		RefPatterns         *[]RefPattern `yaml:"ref_patterns" json:"ref_patterns"`
		TitleStopwords      *[]string     `yaml:"title_stopwords" json:"title_stopwords"`
		StripTitleStemVowel *bool         `yaml:"strip_title_stem_vowel" json:"strip_title_stem_vowel"`
	} `yaml:"chunker" json:"chunker"`

	EntityDescription struct {
		MinDegree   *int `yaml:"min_degree" json:"min_degree"`
		MaxEntities *int `yaml:"max_entities" json:"max_entities"`
	} `yaml:"entity_description" json:"entity_description"`
}

// LayerNote records which layer supplied a field's final value, so a
// three-layer merge can be explained rather than guessed at.
type LayerNote struct {
	// Field is the dotted config path, e.g. "resolution.fold_diacritics".
	Field string
	// Layer is "default", "profile" or "agent.yaml".
	Layer string
	// Value is a short human-readable rendering of what was applied.
	Value string
}

// layerAgentYAML and layerProfile name the override layers in LayerNote.
const (
	layerProfile   = "profile"
	layerAgentYAML = "agent.yaml"
)

// applyTo overlays the set fields onto cfg, appending a LayerNote for each one
// that wins. Fields left nil are untouched, so a lower layer survives.
func (o DomainOverlay) applyTo(cfg *DomainConfig, layer string, notes *[]LayerNote) {
	note := func(field string, value any) {
		if notes != nil {
			*notes = append(*notes, LayerNote{Field: field, Layer: layer, Value: fmt.Sprint(value)})
		}
	}

	// Extraction. The predicate vocabulary is closed: a fact matching no
	// predicate is dropped, so an explicitly empty list would silently discard
	// every fact in the corpus. There is no legitimate reason to want that, and
	// the failure is total and invisible, so it is refused rather than obeyed.
	if p := o.Extraction.Predicates; p != nil {
		if len(*p) == 0 {
			note("extraction.predicates", "ignored empty list — would drop every fact")
		} else {
			cfg.Extraction.Predicates = *p
			note("extraction.predicates", fmt.Sprintf("%d predicates", len(*p)))
		}
	}
	if p := o.Extraction.Priorities; p != nil {
		cfg.Extraction.Priorities = *p
		note("extraction.priorities", fmt.Sprintf("%d priorities", len(*p)))
	}

	// Resolution.
	if p := o.Resolution.Honorifics; p != nil {
		cfg.Resolution.Honorifics = *p
		note("resolution.honorifics", fmt.Sprintf("%d honorifics", len(*p)))
	}
	if p := o.Resolution.FoldDiacritics; p != nil && p.Valid() {
		cfg.Resolution.FoldDiacritics = *p
		note("resolution.fold_diacritics", *p)
	}
	if p := o.Resolution.StripFinalVowel; p != nil {
		cfg.Resolution.StripFinalVowel = *p
		note("resolution.strip_final_vowel", *p)
	}
	if p := o.Resolution.ProperNounPredicates; p != nil {
		cfg.Resolution.ProperNounPredicates = *p
		note("resolution.proper_noun_predicates", fmt.Sprintf("%d predicates", len(*p)))
	}

	// Chunker.
	if p := o.Chunker.RefPatterns; p != nil {
		cfg.Chunker.RefPatterns = *p
		note("chunker.ref_patterns", fmt.Sprintf("%d patterns", len(*p)))
	}
	if p := o.Chunker.TitleStopwords; p != nil {
		cfg.Chunker.TitleStopwords = *p
		note("chunker.title_stopwords", fmt.Sprintf("%d stopwords", len(*p)))
	}
	if p := o.Chunker.StripTitleStemVowel; p != nil {
		cfg.Chunker.StripTitleStemVowel = *p
		note("chunker.strip_title_stem_vowel", *p)
	}

	// Entity descriptions. Zero is not a meaningful value for either — both are
	// counts with a positive floor — so a non-positive value is ignored.
	if p := o.EntityDescription.MinDegree; p != nil && *p > 0 {
		cfg.EntityDescription.MinDegree = *p
		note("entity_description.min_degree", *p)
	}
	if p := o.EntityDescription.MaxEntities; p != nil && *p > 0 {
		cfg.EntityDescription.MaxEntities = *p
		note("entity_description.max_entities", *p)
	}
}

// parseAgentYAMLOverlay reads the domain sections of an agent.yaml. A missing
// or malformed file yields an empty overlay, leaving lower layers intact —
// configuration problems must not take a build or a server down.
func parseAgentYAMLOverlay(path string) DomainOverlay {
	var o DomainOverlay
	data, err := os.ReadFile(path)
	if err != nil {
		return o
	}
	if err := yaml.Unmarshal(data, &o); err != nil {
		return DomainOverlay{}
	}
	return o
}
