package graph

import (
	"strings"
	"unicode"
)

// canonicalPredicates maps surface predicate forms to a single canonical form.
// Without this, "has chapter X" and "contains chapter X" are stored as two
// distinct facts, and a 10-fact retrieval budget gets spent restating one fact.
//
// Only same-direction synonyms are merged. Forms with inverted argument order
// ("authored" vs "was written by") are kept separate — collapsing them would
// silently reverse subject and object.
var canonicalPredicates = map[string]string{
	// containment / structure
	"has":         "contains",
	"have":        "contains",
	"contain":     "contains",
	"contains":    "contains",
	"includes":    "contains",
	"include":     "contains",
	"comprises":   "contains",
	"consists of": "contains",
	"has chapter": "contains",

	"part of":       "is part of",
	"is part of":    "is part of",
	"belongs to":    "is part of",
	"is section of": "is part of",

	// authorship (subject = author)
	"authored":         "authored",
	"author of":        "authored",
	"is author of":     "authored",
	"is the author of": "authored",
	"wrote":            "authored",
	"composed":         "authored",
	"created":          "authored",

	// authorship (subject = work) — inverse direction, kept distinct
	"was written by": "was written by",
	"written by":     "was written by",
	"is authored by": "was written by",
	"authored by":    "was written by",

	// commentary
	"commented on":        "commented on",
	"wrote commentary on": "commented on",
	"is commentary on":    "commented on",
	"is a commentary on":  "commented on",
	"comments on":         "commented on",

	// translation
	"translated":           "translated",
	"translator of":        "translated",
	"is translator of":     "translated",
	"is the translator of": "translated",

	// lineage — the relations a graph over this corpus exists for
	"disciple of":       "was disciple of",
	"is disciple of":    "was disciple of",
	"was disciple of":   "was disciple of",
	"was a disciple of": "was disciple of",
	"student of":        "was disciple of",
	"was student of":    "was disciple of",
	"studied under":     "was disciple of",
	"pupil of":          "was disciple of",

	"teacher of":     "was teacher of",
	"was teacher of": "was teacher of",
	"guru of":        "was teacher of",
	"was guru of":    "was teacher of",
	"taught":         "was teacher of",
	"initiated":      "was teacher of",

	// description
	"describes": "describes",
	"discusses": "describes",
	"explains":  "describes",
	"mentions":  "describes",
	"expounds":  "describes",

	// definition / classification
	"is a type of":  "is a type of",
	"is a kind of":  "is a type of",
	"is a form of":  "is a type of",
	"is defined as": "is defined as",
	"means":         "is defined as",
	"refers to":     "is defined as",
	"is called":     "is defined as",
	"is known as":   "is defined as",

	// causation
	"causes":     "causes",
	"leads to":   "causes",
	"results in": "causes",
	"produces":   "causes",

	// place
	"located in":    "located in",
	"is located in": "located in",
	"is in":         "located in",
	"found in":      "located in",

	// association / requirement
	"associated with":    "associated with",
	"is associated with": "associated with",
	"related to":         "associated with",
	"is related to":      "associated with",
	"requires":           "requires",
	"needs":              "requires",
	"depends on":         "requires",

	// Hindi/Sanskrit copulas that leak in from vernacular source texts
	"hai":  "is a type of",
	"है":   "is a type of",
	"hain": "is a type of",
	"tha":  "is a type of",
}

// noisePredicates are commercial/distribution metadata that add nothing to a
// scholarly knowledge graph but consume extraction and retrieval budget.
var noisePredicates = map[string]bool{
	"distributes":       true,
	"distributed by":    true,
	"is distributed by": true,
	"sells":             true,
	"sold by":           true,
	"is sold by":        true,
	"printed by":        true,
	"is printed by":     true,
	"priced at":         true,
	"costs":             true,
	"has isbn":          true,
	"has price":         true,
}

// CanonicalPredicate reduces a predicate to its canonical surface form.
// Unknown predicates are returned normalized (lowercased, whitespace-collapsed)
// but otherwise unchanged, so the vocabulary stays open.
func CanonicalPredicate(p string) string {
	key := normalizeSurface(p)
	if canon, ok := canonicalPredicates[key]; ok {
		return canon
	}
	return key
}

// IsNoisePredicate reports whether a predicate is commercial metadata.
func IsNoisePredicate(p string) bool {
	return noisePredicates[normalizeSurface(p)]
}

// NormalizeEntity cleans an entity name for storage: trims whitespace,
// collapses internal runs of whitespace, and strips wrapping quotes and
// trailing sentence punctuation. Case and diacritics are preserved — they
// carry meaning in transliterated Sanskrit.
func NormalizeEntity(e string) string {
	e = strings.TrimSpace(e)
	e = strings.Trim(e, `"'`)
	e = strings.TrimRight(e, ".,;:")
	return strings.Join(strings.Fields(e), " ")
}

// FoldKey returns a case- and whitespace-insensitive key for a triple, used to
// collapse near-duplicates such as "Aṣṭādaśaḥ paṭalaḥ" and "aṣṭādaśaḥ paṭalaḥ".
func FoldKey(subject, predicate, object string) string {
	return normalizeSurface(subject) + "|" + CanonicalPredicate(predicate) + "|" + normalizeSurface(object)
}

// normalizeSurface lowercases, collapses whitespace, and strips trailing
// punctuation — the common shape used for both lookups and fold keys.
func normalizeSurface(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimRight(s, ".,;:")
	s = strings.TrimFunc(s, func(r rune) bool {
		return r == '"' || r == '\''
	})
	return strings.Join(strings.Fields(s), " ")
}

// looksLikeMetadataEntity reports whether an entity is a publishing house or
// similar commercial entity rather than a subject of study.
func looksLikeMetadataEntity(e string) bool {
	l := strings.ToLower(e)
	for _, marker := range []string{
		"book sales", "book depot", "publishers", "publishing", "publication",
		"distributors", "booksellers", "printing press", "pvt. ltd", "pvt ltd",
	} {
		if strings.Contains(l, marker) {
			return true
		}
	}
	return false
}

// hasLetters reports whether a string contains at least one letter, filtering
// out junk entities made purely of digits or punctuation.
func hasLetters(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false
}
