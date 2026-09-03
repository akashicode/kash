package reader

import (
	"fmt"
	"strings"
	"unicode"
)

// TextQuality reports whether extracted text looks like real language.
//
// PDFs whose embedded font subsets carry no usable ToUnicode CMap make
// text extractors emit glyph indices rendered as arbitrary letters — text that
// is structurally a substitution cipher. It is valid UTF-8, non-empty, and
// entirely unretrievable, so an emptiness check does not catch it. Two signals
// separate it from real prose in any Latin-script language:
//
//   - Mid-word capitals. Real text puts capitals at token starts; a glyph
//     cipher scatters them uniformly, because whichever glyph index happens to
//     map to 'U' or 'T' appears wherever that glyph occurs.
//   - Token length. The cipher usually maps the space glyph onto a letter, so
//     word boundaries vanish and tokens run very long.
//
// Non-Latin scripts (Devanagari, IAST) are unaffected: they have no case
// distinction to skew, and normal token lengths.
type TextQuality struct {
	// MidWordCapitalRatio is the share of letters in non-initial token
	// positions that are uppercase.
	MidWordCapitalRatio float64
	// MeanTokenLength is the mean rune length of whitespace-separated tokens.
	MeanTokenLength float64
	// LetterRatio is the share of non-space runes that are letters.
	LetterRatio float64
	// Suspect reports whether the text failed a check.
	Suspect bool
	// Reason describes the failure when Suspect is true.
	Reason string
}

// Quality thresholds. These are deliberately loose — the failure mode they
// catch is extreme (ratios around 0.4 and tokens over 25 runes), so a wide
// margin avoids rejecting unusual but genuine text.
// Measured against the live corpus: genuine English prose scores midcap 0.014 /
// meanTok 4.4, IAST-transliterated Sanskrit 0.000 / 4.4, and even dense index
// tables 0.000 / 6.9 — while glyph-cipher text scores 0.188 / 70.5. Both
// thresholds sit far from every genuine sample.
const (
	maxMidWordCapitalRatio = 0.10
	maxMeanTokenLength     = 16.0
	minLetterRatio         = 0.50
	// minSampleRunes gates on runes rather than tokens: the corruption being
	// detected destroys word boundaries, so a token-count gate would be
	// defeated by the very text it needs to judge.
	minSampleRunes = 400
)

// AssessText scores extracted text for signs of glyph-cipher corruption.
func AssessText(text string) TextQuality {
	var (
		tokenCount    int
		tokenRunes    int
		midWordTotal  int
		midWordUpper  int
		letters       int
		nonSpaceRunes int
	)

	for _, token := range strings.Fields(text) {
		tokenCount++
		letterIdx := 0
		for _, r := range token {
			tokenRunes++
			nonSpaceRunes++
			if !unicode.IsLetter(r) {
				continue
			}
			letters++
			// Only cased scripts contribute to the capital signal.
			if unicode.IsUpper(r) || unicode.IsLower(r) {
				if letterIdx > 0 {
					midWordTotal++
					if unicode.IsUpper(r) {
						midWordUpper++
					}
				}
			}
			letterIdx++
		}
	}

	q := TextQuality{}
	if tokenCount == 0 || nonSpaceRunes == 0 {
		q.Suspect = true
		q.Reason = "no textual content"
		return q
	}

	q.MeanTokenLength = float64(tokenRunes) / float64(tokenCount)
	q.LetterRatio = float64(letters) / float64(nonSpaceRunes)
	if midWordTotal > 0 {
		q.MidWordCapitalRatio = float64(midWordUpper) / float64(midWordTotal)
	}

	// Very short samples are not worth judging.
	if nonSpaceRunes < minSampleRunes {
		return q
	}

	switch {
	case q.LetterRatio < minLetterRatio:
		q.Suspect = true
		q.Reason = fmt.Sprintf("only %.0f%% of characters are letters", q.LetterRatio*100)
	case midWordTotal > 200 && q.MidWordCapitalRatio > maxMidWordCapitalRatio:
		q.Suspect = true
		q.Reason = fmt.Sprintf("%.0f%% of mid-word letters are capitals (expected under %.0f%%) — "+
			"the font subset likely has no usable ToUnicode CMap, so the extractor returned glyph indices rather than text",
			q.MidWordCapitalRatio*100, maxMidWordCapitalRatio*100)
	case q.MeanTokenLength > maxMeanTokenLength:
		q.Suspect = true
		q.Reason = fmt.Sprintf("mean word length is %.1f characters (expected under %.0f) — word boundaries appear to be missing",
			q.MeanTokenLength, maxMeanTokenLength)
	}

	return q
}
