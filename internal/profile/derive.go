package profile

import (
	"fmt"

	"github.com/akashicode/kash/internal/config"
)

// Status describes what LoadOrDerive did, so the caller can report it.
type Status string

const (
	// StatusLoaded means an existing profile was reused unchanged.
	StatusLoaded Status = "loaded"
	// StatusDerived means a profile was generated because none existed.
	StatusDerived Status = "derived"
	// StatusRefreshed means an existing profile was regenerated on request.
	StatusRefreshed Status = "refreshed"
	// StatusUnreadable means an existing profile could not be read and was
	// regenerated over.
	StatusUnreadable Status = "unreadable"
)

// Options controls derivation.
type Options struct {
	// Refresh regenerates over an existing profile.
	Refresh bool
	// KashVersion stamps the binary that derived the profile.
	KashVersion string
}

// Derive measures a corpus and returns the profile it implies.
//
// This is the deterministic half only: everything here is a measurement over
// the documents, with no model involved. Fields it cannot settle are left unset
// so the built-in defaults show through, and each decision records the evidence
// behind it.
func Derive(docs []Doc, opts Options) *Profile {
	p := New()
	p.KashVersion = opts.KashVersion

	defaults := config.DefaultDomainConfig()

	// Diacritic folding — exact, from the characters present.
	mode, evidence := DetectDiacritics(docs)
	p.Config.Resolution.FoldDiacritics = &mode
	p.AddSignal("resolution.fold_diacritics", string(mode), DecidedDetected, evidence)

	// Stem-vowel folding — from whether variants actually co-occur.
	sv := DetectStemVowel(docs)
	if sv.Decided {
		v := sv.Value
		p.Config.Resolution.StripFinalVowel = &v
		p.Config.Chunker.StripTitleStemVowel = &v
		p.AddSignal("resolution.strip_final_vowel", fmt.Sprint(v), DecidedDetected, sv.Narrative)
		p.AddSignal("chunker.strip_title_stem_vowel", fmt.Sprint(v), DecidedDetected,
			"mirrors resolution.strip_final_vowel — the same linguistic fact, two consumers")
	} else {
		p.AddSignal("resolution.strip_final_vowel", "default", DecidedDefault, sv.Narrative)
	}

	// Structural reference patterns.
	cands, refEvidence := DetectRefPatterns(docs)
	if len(cands) > 0 {
		patterns := append(ToRefPatterns(cands), defaults.Chunker.RefPatterns...)
		p.Config.Chunker.RefPatterns = &patterns
		p.AddSignal("chunker.ref_patterns", fmt.Sprintf("%d detected + %d generic",
			len(cands), len(defaults.Chunker.RefPatterns)), DecidedDetected, refEvidence)
	} else {
		p.AddSignal("chunker.ref_patterns", "generic defaults", DecidedDefault, refEvidence)
	}

	// Title stopwords for work grouping.
	stopwords, swEvidence := DetectTitleStopwords(docs, defaults.Chunker.TitleStopwords)
	if stopwords != nil {
		p.Config.Chunker.TitleStopwords = &stopwords
		p.AddSignal("chunker.title_stopwords", fmt.Sprintf("%d words", len(stopwords)),
			DecidedDetected, swEvidence)
	} else {
		p.AddSignal("chunker.title_stopwords", "generic defaults", DecidedDefault, swEvidence)
	}

	// Complete stays false: the judgment fields (extraction vocabulary,
	// honorifics) have not been derived. A profile claiming completeness with
	// default predicates would extract an entire corpus with generic vocabulary
	// and never revisit it.
	p.Complete = false
	p.LLMStatus = "not attempted — deterministic detection only"

	return p
}

// LoadOrDerive returns the profile for a corpus, generating one when absent.
//
// A corpus is profiled once. Later builds reuse the result, so retrieval
// behaviour does not shift because three documents were added; Refresh is the
// explicit way to re-derive.
func LoadOrDerive(path string, docs []Doc, opts Options) (*Profile, Status, error) {
	if !opts.Refresh {
		existing, err := Load(path)
		switch {
		case err != nil:
			// An unreadable profile is regenerated rather than fatal: it is
			// derived data, and refusing to build because of it would be worse
			// than rebuilding it.
			p := Derive(docs, opts)
			return p, StatusUnreadable, err
		case existing != nil:
			return existing, StatusLoaded, nil
		}
		return Derive(docs, opts), StatusDerived, nil
	}
	return Derive(docs, opts), StatusRefreshed, nil
}

// Signature summarises the profile fields that are baked into chunk metadata at
// build time, so a later build can detect that the corpus was compiled under
// different rules.
//
// Only fields that change what is written into the index belong here. A change
// to extraction predicates degrades a corpus; a change to reference patterns or
// diacritic folding makes the stored metadata inconsistent with the query path,
// which is worse and needs a rebuild.
func Signature(cfg config.DomainConfig) string {
	h := fmt.Sprintf("fold=%s;", cfg.Resolution.FoldDiacritics)
	for _, p := range cfg.Chunker.RefPatterns {
		h += p.MetaKey + "=" + p.Pattern + ";"
	}
	return shortHash(h)
}

// PredicateSignature summarises the extraction vocabulary. A change here is
// reported but not fatal: existing triples keep the vocabulary they were
// extracted with, which is degraded rather than corrupt.
func PredicateSignature(cfg config.DomainConfig) string {
	h := ""
	for _, p := range cfg.Extraction.Predicates {
		h += p + ";"
	}
	return shortHash(h)
}
