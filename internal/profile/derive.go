package profile

import (
	"context"
	"fmt"
	"strings"

	"github.com/akashicode/kash/internal/chunker"
	"github.com/akashicode/kash/internal/config"
	"github.com/akashicode/kash/internal/llm"
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

// evidenceBudget caps the corpus text handed to the model.
const evidenceBudget = 12000

// maxHonorificCandidates caps the mined list the model filters.
const maxHonorificCandidates = 30

// Suggester is the model-backed half of derivation. Satisfied by *llm.Client;
// an interface so derivation can be tested without a network call.
type Suggester interface {
	SuggestDomainConfig(ctx context.Context, ev llm.DomainEvidence) (llm.DomainSuggestion, error)
}

// Enrich adds the judgment fields to a profile: extraction vocabulary,
// priorities, honorifics, and names for detected numbering schemes.
//
// Failure is never fatal. The profile keeps everything detection established
// and is marked incomplete, so the next build retries the model without redoing
// the measurements. That distinction is the point: a profile marked complete
// with default predicates would extract a whole corpus with generic vocabulary
// and never revisit it.
func Enrich(ctx context.Context, p *Profile, docs []Doc, s Suggester) {
	if s == nil {
		p.LLMStatus = "skipped — no model configured"
		return
	}

	defaults := config.DefaultDomainConfig()
	candidates := MineHonorificCandidates(docs, maxHonorificCandidates)

	var labels []string
	if sig, ok := p.SignalFor("chunker.ref_patterns"); ok && sig.DecidedBy == DecidedDetected {
		labels = refLabelsFromEvidence(sig.Evidence)
	}

	sug, err := s.SuggestDomainConfig(ctx, llm.DomainEvidence{
		FoldDiacritics:      foldModeOf(p),
		NumberingLabels:     labels,
		TitleTokens:         overlayStrings(p.Config.Chunker.TitleStopwords),
		HonorificCandidates: candidates,
		DefaultPredicates:   defaults.Extraction.Predicates,
		Sample:              EvidenceSample(docs, evidenceBudget),
	})
	if err != nil {
		p.Complete = false
		p.LLMStatus = "failed: " + err.Error()
		return
	}

	// Every value is validated against what the model was allowed to produce.
	// The sample is untrusted corpus text and this output becomes configuration
	// that runs against every query.
	predicates := llm.MergePredicates(defaults.Extraction.Predicates, sug.Predicates)
	p.Config.Extraction.Predicates = &predicates
	p.AddSignal("extraction.predicates", fmt.Sprintf("%d predicates", len(predicates)), DecidedLLM,
		fmt.Sprintf("%d generic + %d suggested for this corpus", len(defaults.Extraction.Predicates),
			len(predicates)-len(defaults.Extraction.Predicates)))

	if len(sug.Priorities) > 0 {
		priorities := sug.Priorities
		p.Config.Extraction.Priorities = &priorities
		p.AddSignal("extraction.priorities", fmt.Sprintf("%d priorities", len(priorities)), DecidedLLM,
			"relation types to favour, from the corpus sample")
	}

	if honorifics := llm.FilterToCandidates(sug.Honorifics, candidates); len(honorifics) > 0 {
		p.Config.Resolution.Honorifics = &honorifics
		p.AddSignal("resolution.honorifics", fmt.Sprintf("%d honorifics", len(honorifics)), DecidedLLM,
			fmt.Sprintf("chosen from %d candidates mined from the text", len(candidates)))
	}

	if pnp := llm.SubsetOf(sug.ProperNounPredicates, predicates); len(pnp) > 0 {
		p.Config.Resolution.ProperNounPredicates = &pnp
		p.AddSignal("resolution.proper_noun_predicates", fmt.Sprintf("%d predicates", len(pnp)),
			DecidedLLM, "predicates whose object names a person, work or product")
	}

	if renamed := applyMetaKeyNames(p, sug.MetaKeys); renamed > 0 {
		p.AddSignal("chunker.ref_patterns.meta_keys", fmt.Sprintf("%d renamed", renamed),
			DecidedLLMNamed, "transliterated numbering labels given readable keys")
	}

	p.Complete = true
	p.LLMStatus = ""
}

// applyMetaKeyNames renames detected reference keys using the model's
// suggestions, rejecting anything that is not a safe metadata key.
func applyMetaKeyNames(p *Profile, names map[string]string) int {
	if p.Config.Chunker.RefPatterns == nil || len(names) == 0 {
		return 0
	}
	patterns := *p.Config.Chunker.RefPatterns
	renamed := 0

	for label, key := range names {
		key = strings.ToLower(strings.TrimSpace(key))
		if !metaKeyRe.MatchString(key) || reservedMetaKeys[key] {
			continue
		}
		for i := range patterns {
			// Only rename a key detection could not name itself.
			if patterns[i].MetaKey == chunker.MetaSection &&
				strings.Contains(strings.ToLower(patterns[i].Pattern), strings.ToLower(label)) {
				patterns[i].MetaKey = key
				renamed++
			}
		}
	}
	if renamed > 0 {
		p.Config.Chunker.RefPatterns = &patterns
	}
	return renamed
}

func foldModeOf(p *Profile) string {
	if p.Config.Resolution.FoldDiacritics == nil {
		return string(config.DiacriticLatin)
	}
	return string(*p.Config.Resolution.FoldDiacritics)
}

func overlayStrings(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

// refLabelsFromEvidence pulls the quoted labels out of the reference-detection
// evidence string, so the model can be asked to name them.
func refLabelsFromEvidence(evidence string) []string {
	var out []string
	for _, part := range strings.Split(evidence, `"`) {
		if part != "" && !strings.Contains(part, "(") && !strings.Contains(part, ";") &&
			!strings.Contains(part, " ") {
			out = append(out, part)
		}
	}
	return out
}
