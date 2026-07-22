package graph

import (
	"sort"
	"strings"
	"unicode"
)

// latinFolder maps common European diacritics to plain ASCII, so
// "Kármán"/"Karman" and "Söderberg"/"Soderberg" group together.
var latinFolder = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "ö", "o", "õ", "o", "ø", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u",
	"ç", "c", "ñ", "n", "ý", "y", "ÿ", "y",
	"š", "s", "ž", "z", "ð", "d", "þ", "t",
)

// iastFolder maps IAST/ITRANS transliteration characters to plain ASCII.
// This is deliberately high-recall: in Sanskrit, diacritics ARE meaning-bearing
// (brahma vs brahmā, dhanya vs dhānya), so folding them generates candidates
// for review rather than decisions.
var iastFolder = strings.NewReplacer(
	"ā", "a", "Ā", "a", "ī", "i", "Ī", "i", "ū", "u", "Ū", "u",
	"ṛ", "r", "Ṛ", "r", "ṝ", "r", "ḷ", "l", "ḹ", "l",
	"ṃ", "m", "Ṃ", "m", "ṁ", "m", "ḥ", "h", "Ḥ", "h",
	"ś", "s", "Ś", "s", "ṣ", "s", "Ṣ", "s", "ñ", "n", "Ñ", "n",
	"ṅ", "n", "Ṅ", "n", "ṇ", "n", "Ṇ", "n", "ṭ", "t", "Ṭ", "t",
	"ḍ", "d", "Ḍ", "d", "ḻ", "l", "ē", "e", "ō", "o",
)

// resolver holds the corpus-specific rules for grouping entity variants,
// built from configuration so the same code serves a Sanskrit corpus, an
// aerospace corpus, or anything else.
type resolver struct {
	honorifics      []string
	foldDiacritics  func(string) string
	stripFinalVowel bool
	properNoun      map[string]bool
}

// newResolver builds a resolver from ResolveOptions.
func newResolver(opts ResolveOptions) *resolver {
	r := &resolver{
		honorifics:      opts.Honorifics,
		stripFinalVowel: opts.StripFinalVowel,
		properNoun:      map[string]bool{},
	}

	switch opts.FoldDiacritics {
	case DiacriticNone:
		r.foldDiacritics = func(s string) string { return s }
	case DiacriticIAST:
		r.foldDiacritics = iastFolder.Replace
	case DiacriticBoth:
		r.foldDiacritics = func(s string) string { return latinFolder.Replace(iastFolder.Replace(s)) }
	default: // DiacriticLatin, and anything unrecognised
		r.foldDiacritics = latinFolder.Replace
	}

	for _, p := range opts.ProperNounPredicates {
		r.properNoun[CanonicalPredicate(p)] = true
	}
	return r
}

// stripHonorific removes a single leading honorific title.
func (r *resolver) stripHonorific(s string) string {
	for _, h := range r.honorifics {
		if rest, ok := strings.CutPrefix(s, h); ok {
			return strings.TrimSpace(rest)
		}
	}
	return s
}

// hasHonorific reports whether a surface form carries a leading title.
func (r *resolver) hasHonorific(s string) bool {
	n := normalizeSurface(s)
	return r.stripHonorific(n) != n
}

// stripStemVowel drops a trailing stem vowel when the corpus uses a convention
// that permits it (Sanskrit: Gorakhnath/Gorakhnatha). Off by default, because
// in most languages dropping a final "a" changes the word.
func (r *resolver) stripStemVowel(s string) string {
	if !r.stripFinalVowel {
		return s
	}
	if len(s) > 5 && strings.HasSuffix(s, "a") && !strings.HasSuffix(s, "aa") {
		return s[:len(s)-1]
	}
	return s
}

// safeKey applies only transformations that cannot change meaning: case,
// whitespace, honorific titles and (where enabled) the stem vowel.
// Diacritics are preserved.
func (r *resolver) safeKey(s string) string {
	return r.stripStemVowel(r.stripHonorific(normalizeSurface(s)))
}

// blockingKey is the high-recall grouping key. It additionally folds
// diacritics, so it may group genuinely distinct words — which is why
// clusters formed only at this level need review.
func (r *resolver) blockingKey(s string) string {
	return r.stripStemVowel(r.stripHonorific(r.foldDiacritics(normalizeSurface(s))))
}

// entityStat accumulates what is known about one surface form.
type entityStat struct {
	surface    string
	degree     int
	properNoun bool
}

// DiacriticMode selects which alphabets' diacritics are folded when grouping
// entity variants. Mirrors config.DiacriticMode so this package stays free of
// a config dependency.
type DiacriticMode = string

// Diacritic folding modes.
const (
	DiacriticNone  DiacriticMode = "none"
	DiacriticLatin DiacriticMode = "latin"
	DiacriticIAST  DiacriticMode = "iast"
	DiacriticBoth  DiacriticMode = "both"
)

// ResolveOptions tunes cluster building. The zero value is not useful; build
// one with DefaultResolveOptions and override from configuration.
type ResolveOptions struct {
	// MinDegree skips clusters whose members appear in fewer than this many
	// triples in total. Measured on the merged entity, since merging two
	// singleton variants produces a degree-2 node that can form a chain.
	MinDegree int
	// Honorifics are leading titles stripped when comparing names.
	Honorifics []string
	// FoldDiacritics selects which diacritics are folded: none, latin, iast, both.
	FoldDiacritics DiacriticMode
	// StripFinalVowel enables Sanskrit-style stem-vowel folding.
	StripFinalVowel bool
	// ProperNounPredicates identify entities that are people, works or
	// products, whose diacritic variants are safe to merge automatically.
	ProperNounPredicates []string
}

// DefaultResolveOptions returns domain-neutral defaults: Latin diacritics
// folded, no stem-vowel rule, generic English titles.
func DefaultResolveOptions() ResolveOptions {
	return ResolveOptions{
		MinDegree:            2,
		Honorifics:           []string{"the ", "a ", "an ", "dr. ", "dr ", "prof. ", "prof ", "mr. ", "mrs. ", "ms. ", "sir "},
		FoldDiacritics:       DiacriticLatin,
		ProperNounPredicates: []string{"created by", "authored", "was written by", "designed by", "developed by"},
	}
}

// BuildClusters groups entity surface forms that likely name the same thing.
//
// Clusters whose members differ only by case, whitespace, honorific or (where
// the corpus enables it) stem vowel are auto-approved — those transformations
// cannot change meaning. Clusters that required diacritic folding are
// auto-approved only when the entity is a proper noun; otherwise they are held
// for review, since in many languages diacritics distinguish real words.
func BuildClusters(triples []SearchResult, opts ResolveOptions) []Cluster {
	if opts.MinDegree <= 0 {
		opts.MinDegree = 1
	}
	res := newResolver(opts)

	stats := map[string]*entityStat{}
	note := func(surface, predicate string) {
		key := normalizeSurface(surface)
		if key == "" {
			return
		}
		st := stats[key]
		if st == nil {
			st = &entityStat{surface: surface}
			stats[key] = st
		}
		st.degree++
		// Prefer the most diacriticized surface form for display
		if diacriticCount(surface) > diacriticCount(st.surface) {
			st.surface = surface
		}
		if res.properNoun[CanonicalPredicate(predicate)] {
			st.properNoun = true
		}
	}
	for _, t := range triples {
		note(t.Subject, t.Predicate)
		note(t.Object, t.Predicate)
	}

	// Group every entity by blocking key BEFORE applying the degree filter.
	// Fragmentation is precisely what splits one entity's degree across its
	// spelling variants, so filtering first would discard the very entities
	// worth merging (three variants of Gorakhnath at degree 1 each).
	groups := map[string][]*entityStat{}
	for _, st := range stats {
		k := res.blockingKey(st.surface)
		if k == "" {
			continue
		}
		groups[k] = append(groups[k], st)
	}

	clusters := make([]Cluster, 0, 64)
	for key, members := range groups {
		if len(members) < 2 {
			continue
		}
		// Degree is measured on the merged entity, not on any one variant
		total := 0
		for _, m := range members {
			total += m.degree
		}
		if total < opts.MinDegree {
			continue
		}

		// Canonical form, in priority order:
		//  1. bare name over a titled one — an honorific's own diacritics
		//     ("ācārya") must not win the title of canonical
		//  2. most diacritics — prefer the scholarly transliteration
		//  3. most frequent, then alphabetical for determinism
		sort.Slice(members, func(i, j int) bool {
			hi, hj := res.hasHonorific(members[i].surface), res.hasHonorific(members[j].surface)
			if hi != hj {
				return !hi
			}
			di, dj := diacriticCount(members[i].surface), diacriticCount(members[j].surface)
			if di != dj {
				return di > dj
			}
			if members[i].degree != members[j].degree {
				return members[i].degree > members[j].degree
			}
			return members[i].surface < members[j].surface
		})

		canonical := members[0].surface
		aliases := make([]string, 0, len(members)-1)
		for _, m := range members[1:] {
			aliases = append(aliases, m.surface)
		}

		approved, reason := res.judge(members)
		clusters = append(clusters, Cluster{
			Key:       key,
			Canonical: canonical,
			Aliases:   aliases,
			Approved:  approved,
			Reason:    reason,
		})
	}

	sort.Slice(clusters, func(i, j int) bool { return clusters[i].Key < clusters[j].Key })
	return clusters
}

// judge decides whether a cluster can be auto-approved.
func (r *resolver) judge(members []*entityStat) (bool, string) {
	// Did the cluster form without folding diacritics?
	base := r.safeKey(members[0].surface)
	safeOnly := true
	for _, m := range members[1:] {
		if r.safeKey(m.surface) != base {
			safeOnly = false
			break
		}
	}
	if safeOnly {
		return true, "case/honorific/stem-vowel variant"
	}

	for _, m := range members {
		if m.properNoun {
			return true, "proper noun (person or titled work)"
		}
	}
	return false, "differs by diacritics — review: diacritics distinguish Sanskrit words"
}

// diacriticCount counts non-ASCII letters, used to prefer the scholarly
// transliteration as the canonical form.
func diacriticCount(s string) int {
	n := 0
	for _, r := range s {
		if r > unicode.MaxASCII {
			n++
		}
	}
	return n
}

// MergeClusters combines newly built clusters with an existing file,
// preserving hand-edited decisions. Existing clusters keep their Approved,
// Canonical and Note values; only the alias list is refreshed.
func MergeClusters(existing, fresh []Cluster) []Cluster {
	byKey := map[string]Cluster{}
	for _, c := range existing {
		byKey[c.Key] = c
	}

	out := make([]Cluster, 0, len(fresh))
	for _, f := range fresh {
		if prev, ok := byKey[f.Key]; ok {
			// Respect manual edits: keep the human's canonical and verdict
			f.Approved = prev.Approved
			f.Note = prev.Note
			if prev.Canonical != "" {
				f.Canonical = prev.Canonical
				f.Aliases = mergeAliases(f, prev.Canonical)
			}
			if prev.Reason != "" {
				f.Reason = prev.Reason
			}
			delete(byKey, f.Key)
		}
		out = append(out, f)
	}

	// Keep hand-written clusters that no longer match any blocking key
	for _, leftover := range byKey {
		leftover.Note = strings.TrimSpace(leftover.Note + " [no longer present in graph]")
		out = append(out, leftover)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// mergeAliases rebuilds the alias list around a user-chosen canonical form.
func mergeAliases(c Cluster, canonical string) []string {
	all := append([]string{c.Canonical}, c.Aliases...)
	canonKey := normalizeSurface(canonical)
	out := make([]string, 0, len(all))
	seen := map[string]bool{}
	for _, a := range all {
		k := normalizeSurface(a)
		if k == canonKey || k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}
