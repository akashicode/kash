package chunker

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Metadata keys attached to every chunk. These exist so retrieval can filter and
// rank on structure rather than on embedding similarity alone — a query naming a
// verse number carries almost no signal in a dense embedding, but is an exact
// match against MetaVerse.
const (
	// MetaBook is the work the chunk came from, derived from the filename.
	MetaBook = "book"
	// MetaHeading is the innermost heading covering the chunk.
	MetaHeading = "heading"
	// MetaBreadcrumb is the full heading path, joined with " > ".
	MetaBreadcrumb = "breadcrumb"
	// MetaVerse is a verse number parsed from the heading or body.
	MetaVerse = "verse"
	// MetaDharana is a dhāraṇā number, numbered separately from verses in
	// several Vijñāna Bhairava editions.
	MetaDharana = "dharana"
	// MetaContentType is one of the contentType* constants.
	MetaContentType = "content_type"
	// MetaNoiseScore is a 0..1 estimate of how much this chunk looks like
	// apparatus (index tables, concordances, page listings) rather than prose.
	MetaNoiseScore = "noise_score"
)

// Content classifications.
const (
	ContentProse = "prose"
	ContentTable = "table"
	ContentIndex = "index"
)

var (
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	fenceRe   = regexp.MustCompile("^\\s*(```|~~~)")

	verseHeadingRe = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:verse|śloka|shloka|sloka)\s*[-–—]?\s*(\d+)`)
	// The 112 techniques are numbered as "dhāraṇā-49" in the Sanskrit editions
	// and as "vidhi 01" in the Osho/Hindi edition; they are the same thing, and
	// a reader asking for one by number must reach either.
	dharanaHeadingRe = regexp.MustCompile(`(?i)(?:dh[aā]ra[nṇ][aā]|vidhi)\s*[-–—]?\s*(\d+)`)

	// Sanskrit verse markers as they appear in this corpus: "..71..", "|| 40 ||",
	// and the Devanagari double danda "॥ 14 ॥".
	verseBodyRe = regexp.MustCompile(`(?:\.\.|\|\||॥)\s*(\d+)\s*(?:\.\.|\|\||॥)`)

	// Editions that number techniques as a bare "32)" rather than "Verse 32".
	// The English Vijñāna Bhairava uses this for most of its 112 techniques —
	// missing it made those techniques unaddressable by number even though the
	// text was fully present.
	parenHeadingRe = regexp.MustCompile(`^\s*(\d{1,3})\)`)
	parenBodyRe    = regexp.MustCompile(`(?m)^\s{0,3}(\d{1,3})\)\s`)

	tableRowRe = regexp.MustCompile(`^\s*\|.*\|\s*$`)
	// A concordance/index row: some text, then one or more page numbers.
	indexRowRe = regexp.MustCompile(`^\s*\S.*?\s+\d+(\s*[,/]\s*\d+)*\s*$`)
)

// section is a heading-delimited span of a document.
type section struct {
	breadcrumb []string
	heading    string
	body       string
}

// parseSections splits markdown into heading-delimited sections, tracking the
// heading stack so each section knows its full path. Text before the first
// heading becomes a section with an empty heading, and a document with no
// headings at all yields exactly one such section — which is how plain .txt
// files degrade gracefully to the unstructured path.
func parseSections(text string) []section {
	lines := strings.Split(text, "\n")

	var (
		sections []section
		stack    []string // heading text indexed by level-1
		levels   []int
		cur      strings.Builder
		curHead  string
		inFence  bool
	)

	flush := func() {
		body := strings.TrimSpace(cur.String())
		cur.Reset()
		if body == "" && curHead == "" {
			return
		}
		crumb := make([]string, len(stack))
		copy(crumb, stack)
		sections = append(sections, section{breadcrumb: crumb, heading: curHead, body: body})
	}

	for _, line := range lines {
		if fenceRe.MatchString(line) {
			inFence = !inFence
			cur.WriteString(line)
			cur.WriteString("\n")
			continue
		}
		if inFence {
			cur.WriteString(line)
			cur.WriteString("\n")
			continue
		}

		m := headingRe.FindStringSubmatch(line)
		if m == nil {
			cur.WriteString(line)
			cur.WriteString("\n")
			continue
		}

		flush()

		level := len(m[1])
		title := strings.TrimSpace(m[2])

		// Pop any headings at or below this level, then push this one.
		for len(levels) > 0 && levels[len(levels)-1] >= level {
			levels = levels[:len(levels)-1]
			stack = stack[:len(stack)-1]
		}
		levels = append(levels, level)
		stack = append(stack, title)
		curHead = title
	}
	flush()

	return sections
}

// breakScore rates a line as a place to split, mirroring zvec-grep's scored
// break points. Higher is a better cut.
func breakScore(prev, cur string) int {
	switch {
	case headingRe.MatchString(cur):
		return 100
	case prev == "" && cur == "":
		return 70
	case prev == "":
		return 60
	case strings.HasPrefix(strings.TrimSpace(cur), "- "),
		strings.HasPrefix(strings.TrimSpace(cur), "* "),
		strings.HasPrefix(strings.TrimSpace(cur), "+ "):
		return 35
	case strings.HasPrefix(strings.TrimSpace(cur), ">"):
		return 25
	default:
		return 10
	}
}

// splitScored cuts text into windows of at most size runes, preferring the
// highest-scoring break point found in the last 30% of each window. This keeps
// a heading with the text it introduces instead of severing them.
func splitScored(text string, size, overlap int) []string {
	lines := strings.Split(text, "\n")
	if size <= 0 {
		return []string{text}
	}

	var (
		out     []string
		cur     []string
		curLen  int
		lastLen int
	)

	emit := func() {
		if len(cur) == 0 {
			return
		}
		chunk := strings.TrimSpace(strings.Join(cur, "\n"))
		if chunk != "" {
			out = append(out, chunk)
		}
		// Carry an overlap tail of whole lines.
		cur, curLen = carryTail(cur, overlap)
		lastLen = curLen
	}

	for i, line := range lines {
		lineLen := utf8.RuneCountInString(line) + 1
		if curLen > 0 && curLen+lineLen > size {
			// Look back over the tail of the window for a better cut.
			if best := bestBreak(cur, size); best > 0 && best < len(cur) {
				held := cur[best:]
				cur = cur[:best]
				emit()
				cur = append(cur, held...)
				curLen = lastLen + runeLenLines(held)
			} else {
				emit()
			}
		}
		cur = append(cur, line)
		curLen += lineLen
		_ = i
	}
	emit()

	// emit() seeds cur with an overlap tail; drop a trailing carry-only window.
	return out
}

// bestBreak returns the index in lines of the best cut point within the last
// 30% of the window, or 0 when no line beats the baseline.
func bestBreak(lines []string, size int) int {
	if len(lines) < 2 {
		return 0
	}
	start := len(lines) * 7 / 10
	if start < 1 {
		start = 1
	}

	bestIdx, bestScore := 0, 10
	for i := start; i < len(lines); i++ {
		prev := strings.TrimSpace(lines[i-1])
		score := breakScore(prev, lines[i])
		if score > bestScore {
			bestScore, bestIdx = score, i
		}
	}
	return bestIdx
}

func runeLenLines(lines []string) int {
	n := 0
	for _, l := range lines {
		n += utf8.RuneCountInString(l) + 1
	}
	return n
}

// carryTail returns the trailing lines of a window totalling at most overlap
// runes, so consecutive chunks share context.
func carryTail(lines []string, overlap int) ([]string, int) {
	if overlap <= 0 {
		return nil, 0
	}
	total := 0
	for i := len(lines) - 1; i >= 0; i-- {
		n := utf8.RuneCountInString(lines[i]) + 1
		if total+n > overlap {
			tail := make([]string, len(lines)-i-1)
			copy(tail, lines[i+1:])
			return tail, total
		}
		total += n
	}
	return nil, 0
}

// classify estimates whether a chunk is prose or apparatus — index tables,
// concordances, page listings. These are term-dense, so they score moderately
// against almost any query and crowd genuine prose out of a small result slate.
// They are scored rather than dropped: a concordance is still legitimate content
// for a lookup, it just should not outrank an actual explanation.
func classify(content string) (string, float64) {
	lines := strings.Split(content, "\n")
	var (
		nonEmpty  int
		tableRows int
		indexRows int
		digits    int
		letters   int
	)

	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		nonEmpty++
		if tableRowRe.MatchString(t) {
			tableRows++
		} else if indexRowRe.MatchString(t) && utf8.RuneCountInString(t) < 120 {
			indexRows++
		}
	}
	for _, r := range content {
		switch {
		case unicode.IsDigit(r):
			digits++
		case unicode.IsLetter(r):
			letters++
		}
	}

	if nonEmpty == 0 {
		return ContentProse, 0
	}

	tableShare := float64(tableRows) / float64(nonEmpty)
	indexShare := float64(indexRows) / float64(nonEmpty)
	digitShare := 0.0
	if letters+digits > 0 {
		digitShare = float64(digits) / float64(letters+digits)
	}

	// Blend the signals; each alone is weak, together they are decisive.
	noise := tableShare*0.8 + indexShare*0.7 + digitShare*1.5
	if noise > 1 {
		noise = 1
	}

	switch {
	case tableShare > 0.5:
		return ContentTable, noise
	case indexShare > 0.5 || digitShare > 0.25:
		return ContentIndex, noise
	default:
		return ContentProse, noise
	}
}

// bookTitle derives a human-readable work title from a source filename.
func bookTitle(source string) string {
	name := source
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	if i := strings.LastIndex(name, "."); i > 0 {
		name = name[:i]
	}
	for _, suffix := range []string{"_FINAL_iast", "_FINAL", "_OCR", "_iast"} {
		name = strings.TrimSuffix(name, suffix)
	}
	name = strings.NewReplacer("-", " ", "_", " ").Replace(name)

	fields := strings.Fields(name)
	for i, f := range fields {
		r := []rune(f)
		// Title-case only all-lowercase words, so "VijnanaBhairava" and IAST
		// spellings keep their original casing.
		if unicode.IsLower(r[0]) {
			r[0] = unicode.ToUpper(r[0])
			fields[i] = string(r)
		}
	}
	return strings.Join(fields, " ")
}

// extractRefs pulls verse and dhāraṇā numbers from a heading and body.
func extractRefs(heading, body string) (verse, dharana string) {
	if m := dharanaHeadingRe.FindStringSubmatch(heading); m != nil {
		dharana = m[1]
	}
	if m := verseHeadingRe.FindStringSubmatch(heading); m != nil {
		verse = m[1]
	}
	if verse == "" {
		if m := parenHeadingRe.FindStringSubmatch(heading); m != nil {
			verse = m[1]
		}
	}
	if verse == "" {
		if m := verseBodyRe.FindStringSubmatch(body); m != nil {
			verse = m[1]
		}
	}
	return verse, dharana
}

// contextHeader renders the citation header prefixed to a chunk. It is kept
// short and is deliberately part of the stored text: it gives the answering
// model the reference it needs to cite a verse, and gives the embedding a
// little topical anchoring.
func contextHeader(meta map[string]string) string {
	var parts []string
	if b := meta[MetaBook]; b != "" {
		parts = append(parts, b)
	}
	for _, crumb := range strings.Split(meta[MetaBreadcrumb], " > ") {
		crumb = strings.TrimSpace(crumb)
		// Book titles and the document's H1 are usually the same text; repeating
		// it wastes header budget and reads badly in a citation.
		if crumb == "" || containsFold(parts, crumb) {
			continue
		}
		parts = append(parts, crumb)
	}
	// Only state a reference the heading path does not already carry.
	if v := meta[MetaVerse]; v != "" && !anyHasNumber(parts, v) {
		parts = append(parts, "Verse "+v)
	}
	if d := meta[MetaDharana]; d != "" && !anyHasNumber(parts, d) {
		parts = append(parts, "Dharana "+d)
	}
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, " > ") + "]"
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// anyHasNumber reports whether any part already cites this number, so the
// header does not repeat "Verse 25" after a heading that reads "Verse 25".
func anyHasNumber(parts []string, num string) bool {
	for _, p := range parts {
		for _, f := range strings.FieldsFunc(p, func(r rune) bool { return !unicode.IsDigit(r) }) {
			if f == num {
				return true
			}
		}
	}
	return false
}

// maxHeaderRatio caps the share of a chunk's budget the context header may take,
// following zvec-grep's MAX_METADATA_BUDGET_RATIO. The header is deducted from
// the content budget before splitting, so annotated chunks cannot silently
// exceed the embedder's limit.
const maxHeaderRatio = 0.25

// unit is one indivisible piece of text plus the section context it came from.
// Sections are turned into units first and packed into chunks second: emitting
// one chunk per section would fragment a verse-per-heading text into thousands
// of tiny chunks, roughly doubling embedding cost and splitting each verse from
// the commentary that explains it.
type unit struct {
	text       string
	breadcrumb string
	heading    string
}

// SplitStructured chunks a document along its heading structure, attaching
// citation metadata to every chunk.
//
// Documents without headings degrade to the same scored line splitting, which
// is still a strict improvement on blind character splitting: it cuts at
// paragraph boundaries rather than mid-word.
func (c *Chunker) SplitStructured(text, source string) ([]Chunk, error) {
	if !utf8.ValidString(text) {
		return nil, fmt.Errorf("text is not valid UTF-8")
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")

	book := bookTitle(source)

	var units []unit

	for _, sec := range parseSections(text) {
		// A heading with no body of its own is pure structure; its text already
		// reaches its children through their breadcrumb, so emitting it as a
		// chunk would just spend a retrieval slot on a title.
		if strings.TrimSpace(sec.body) == "" {
			continue
		}

		breadcrumb := strings.Join(sec.breadcrumb, " > ")

		// Reserve worst-case header space out of the budget before splitting, so
		// an annotated chunk cannot exceed the embedder's limit.
		headerLen := utf8.RuneCountInString(contextHeader(map[string]string{
			MetaBook: book, MetaBreadcrumb: breadcrumb,
		}))
		if maxLen := int(float64(c.opts.ChunkSize) * maxHeaderRatio); headerLen > maxLen {
			headerLen = maxLen
		}
		budget := c.opts.ChunkSize - headerLen - 2
		if budget < 200 {
			budget = 200
		}

		body := sec.body
		if sec.heading != "" {
			body = sec.heading + "\n\n" + body
		}

		for _, piece := range splitScored(body, budget, c.opts.Overlap) {
			units = append(units, unit{text: piece, breadcrumb: breadcrumb, heading: sec.heading})
		}
	}

	var (
		chunks []Chunk
		idx    int
		buf    []unit
		bufLen int
	)

	flush := func() {
		if len(buf) == 0 {
			return
		}
		texts := make([]string, len(buf))
		for i, u := range buf {
			texts[i] = u.text
		}
		body := strings.Join(texts, "\n\n")

		meta := map[string]string{
			MetaBook:       book,
			MetaHeading:    buf[0].heading,
			MetaBreadcrumb: buf[0].breadcrumb,
		}
		// Record every reference in the chunk, not just the first: a packed
		// chunk can span several verses, and an exact-lookup route needs to
		// match any of them.
		if v := collectRefs(body, verseBodyRe, verseHeadingRe, buf); v != "" {
			meta[MetaVerse] = v
		}
		if d := collectDharanas(buf); d != "" {
			meta[MetaDharana] = d
		}

		ctype, noise := classify(body)
		meta[MetaContentType] = ctype
		meta[MetaNoiseScore] = fmt.Sprintf("%.2f", noise)

		content := body
		if h := contextHeader(meta); h != "" {
			content = h + "\n\n" + body
		}

		chunks = append(chunks, Chunk{
			ID:       buildChunkID(source, idx),
			Content:  content,
			Source:   source,
			Index:    idx,
			Metadata: meta,
		})
		idx++
		buf, bufLen = nil, 0
	}

	for _, u := range units {
		n := utf8.RuneCountInString(u.text)

		// Start a new chunk when this unit opens a numbered verse or technique
		// and the buffer already covers a different one. Packing keeps a verse
		// with the commentary that explains it, but merging "Verse 25" into
		// "Verse 26" would make neither addressable by number — which is the
		// precise failure this metadata exists to fix.
		if bufLen > 0 && startsNewReference(buf, u) {
			flush()
		} else if bufLen > 0 && bufLen+n+2 > c.opts.ChunkSize {
			flush()
		}

		buf = append(buf, u)
		bufLen += n + 2
	}
	flush()

	return chunks, nil
}

// startsNewReference reports whether u opens a numbered verse or technique that
// differs from the one the buffer already covers.
func startsNewReference(buf []unit, u unit) bool {
	uv := headingRef(u.heading)
	if uv == "" {
		return false
	}
	for _, b := range buf {
		if r := headingRef(b.heading); r != "" && r != uv {
			return true
		}
	}
	return false
}

// headingRef returns a heading's verse or technique reference, if it has one.
func headingRef(heading string) string {
	if m := dharanaHeadingRe.FindStringSubmatch(heading); m != nil {
		return "d" + m[1]
	}
	if m := verseHeadingRe.FindStringSubmatch(heading); m != nil {
		return "v" + m[1]
	}
	if m := parenHeadingRe.FindStringSubmatch(heading); m != nil {
		return "v" + m[1]
	}
	return ""
}

// collectRefs gathers every verse number appearing in a packed chunk, in order,
// deduplicated, as a comma-separated list.
func collectRefs(body string, bodyRe, headRe *regexp.Regexp, units []unit) string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, u := range units {
		if m := headRe.FindStringSubmatch(u.heading); m != nil {
			add(m[1])
		} else if m := parenHeadingRe.FindStringSubmatch(u.heading); m != nil {
			add(m[1])
		}
	}
	for _, m := range bodyRe.FindAllStringSubmatch(body, -1) {
		add(m[1])
	}
	// Editions that mark techniques as a bare "32)" at the start of a line.
	// Only consulted when nothing else numbered the chunk, because an ordinary
	// numbered list would otherwise be read as verse numbers.
	if len(out) == 0 {
		for _, m := range parenBodyRe.FindAllStringSubmatch(body, -1) {
			add(m[1])
		}
	}
	return strings.Join(out, ",")
}

// collectDharanas gathers dhāraṇā numbers from the headings in a packed chunk.
func collectDharanas(units []unit) string {
	var out []string
	seen := map[string]bool{}
	for _, u := range units {
		if m := dharanaHeadingRe.FindStringSubmatch(u.heading); m != nil && !seen[m[1]] {
			seen[m[1]] = true
			out = append(out, m[1])
		}
	}
	return strings.Join(out, ",")
}
