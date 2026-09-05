package chunker

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/akashicode/kash/internal/config"
)

// Metadata keys attached to every chunk. These exist so retrieval can filter
// and rank on structure rather than on embedding similarity alone — a query
// naming a section number carries almost no signal in a dense embedding, but
// is an exact match against the right metadata key.
const (
	// MetaBook is the work the chunk came from, derived from the filename.
	MetaBook = "book"
	// MetaHeading is the innermost heading covering the chunk.
	MetaHeading = "heading"
	// MetaBreadcrumb is the full heading path, joined with " > ".
	MetaBreadcrumb = "breadcrumb"
	// MetaSection is the generic structural reference number (Section 4.2,
	// Clause 7, Article 12, …). Domain-specific patterns (verse, dharana)
	// write to whatever meta_key the agent.yaml declares — this constant is
	// the key used by the built-in generic defaults.
	MetaSection = "section"
	// MetaContentType is one of the contentType* constants.
	MetaContentType = "content_type"
	// MetaNoiseScore is a 0..1 estimate of how much this chunk looks like
	// apparatus (index tables, concordances, page listings) rather than prose.
	MetaNoiseScore = "noise_score"

	// MetaVerse and MetaDharana are kept as deprecated aliases for the string
	// literals they wrap, so existing tantra-expert configurations that read
	// these metadata keys from their own agent.yaml ref_patterns still work.
	// New code should use the meta_key value from config.RefPattern directly.
	MetaVerse   = "verse"
	MetaDharana = "dharana"
)

// Content classifications.
const (
	ContentProse = "prose"
	ContentTable = "table"
	ContentIndex = "index"
)

// structuralMetaKeys is the set of chunk metadata keys that are domain-neutral
// infrastructure. contextHeader skips these and only renders user-defined
// reference keys.
var structuralMetaKeys = map[string]bool{
	MetaBook:        true,
	MetaHeading:     true,
	MetaBreadcrumb:  true,
	MetaContentType: true,
	MetaNoiseScore:  true,
}

// refMatcher pairs a compiled regexp (with one capture group for the number)
// with the metadata key to write.
type refMatcher struct {
	re      *regexp.Regexp
	metaKey string
}

var (
	headingRe  = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	fenceRe    = regexp.MustCompile("^\\s*(```|~~~)")
	tableRowRe = regexp.MustCompile(`^\s*\|.*\|\s*$`)
	// A concordance/index row: some text, then one or more page numbers.
	indexRowRe = regexp.MustCompile(`^\s*\S.*?\s+\d+(\s*[,/]\s*\d+)*\s*$`)
	// Table separator row: |---|---|  or  |:--|:--:|
	tableSepRe = regexp.MustCompile(`^\s*\|(?:\s*:?-+:?\s*\|)+\s*$`)
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
func splitScored(text string, size, overlap int) []scoredPiece {
	lines := strings.Split(text, "\n")
	if size <= 0 {
		return []scoredPiece{{text: text}}
	}

	var (
		out     []scoredPiece
		cur     []string
		curLen  int
		lastLen int
		carry   string // leading text of cur repeated from the previous window
	)

	emit := func() {
		if len(cur) == 0 {
			return
		}
		chunk := strings.TrimSpace(strings.Join(cur, "\n"))
		// A window holding nothing but the carried tail repeats the previous
		// piece and adds no text of its own. Emitting it stores the same
		// passage twice — this is the guard this function has always claimed
		// to have.
		if chunk != "" && hasTextBeyond(cur, carry) {
			out = append(out, scoredPiece{text: chunk, carry: carry})
		}
		// Carry an overlap tail of whole lines.
		var tail []string
		tail, curLen = carryTail(cur, overlap)
		cur = tail
		carry = strings.TrimSpace(strings.Join(tail, "\n"))
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
	return out
}

// scoredPiece is one window of a section body, plus the leading text it repeats
// from the window before it.
//
// The repetition is deliberate — consecutive chunks share an overlap tail so a
// passage split across a boundary stays readable in both. It becomes a defect
// only when two consecutive pieces are packed into the *same* chunk, where the
// carried text would be stored twice. Recording what was carried, at the point
// it is carried, is what lets the packer strip it there and only there.
type scoredPiece struct {
	text string
	// carry is the leading portion of text repeated from the previous piece,
	// trimmed. Empty for the first piece of a section.
	carry string
}

// hasTextBeyond reports whether lines hold any non-blank content beyond the
// carried prefix.
func hasTextBeyond(lines []string, carry string) bool {
	if carry == "" {
		return true
	}
	return stripCarry(strings.TrimSpace(strings.Join(lines, "\n")), carry) != ""
}

// stripCarry removes the carried prefix from a piece, comparing line by line
// and ignoring blank lines on either side.
//
// It returns text unchanged unless the piece genuinely begins with the carry.
// That conservatism is the point: a corpus repeats lines for real reasons — a
// refrain, a table header, a digitisation footer on every page — and those must
// survive. Only text known to have been carried is removed.
func stripCarry(text, carry string) string {
	if carry == "" {
		return text
	}
	textLines := strings.Split(text, "\n")
	carryLines := strings.Split(carry, "\n")

	i, j := 0, 0
	for i < len(textLines) && j < len(carryLines) {
		if strings.TrimSpace(textLines[i]) == "" {
			i++
			continue
		}
		if strings.TrimSpace(carryLines[j]) == "" {
			j++
			continue
		}
		if strings.TrimSpace(textLines[i]) != strings.TrimSpace(carryLines[j]) {
			return text
		}
		i++
		j++
	}
	for ; j < len(carryLines); j++ {
		if strings.TrimSpace(carryLines[j]) != "" {
			return text // the piece ended before the carry did
		}
	}
	return strings.TrimSpace(strings.Join(textLines[i:], "\n"))
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

// extractRefs runs every refMatcher against each heading and the body, returning a
// map from meta_key to comma-separated list of matched numbers.
//
// Headings take precedence per meta key: if a heading already answered a key,
// the body is not scanned for that key. A heading names what the chunk *is*,
// whereas the body may merely mention other references — most commonly an
// ordinary numbered list, which must not become verse or clause numbering just
// because it shares the paren form.
//
// The body is still scanned for every key no heading answered. That is what
// makes a chunk holding a run of references — a verse listing, a schedule of
// clauses — addressable by each of them rather than only by its heading.
func extractRefs(headings []string, body string, matchers []refMatcher) map[string][]string {
	out := map[string][]string{}
	seen := map[string]map[string]bool{}

	add := func(key, val string) {
		if val == "" {
			return
		}
		if seen[key] == nil {
			seen[key] = map[string]bool{}
		}
		if !seen[key][val] {
			seen[key][val] = true
			out[key] = append(out[key], val)
		}
	}

	for _, m := range matchers {
		for _, h := range headings {
			for _, hit := range m.re.FindAllStringSubmatch(h, -1) {
				add(m.metaKey, hit[1])
			}
		}
	}

	answeredByHeading := make(map[string]bool, len(out))
	for key := range out {
		answeredByHeading[key] = true
	}

	for _, m := range matchers {
		if answeredByHeading[m.metaKey] {
			continue
		}
		for _, hit := range m.re.FindAllStringSubmatch(body, -1) {
			add(m.metaKey, hit[1])
		}
	}
	return out
}

// contextHeader renders the citation header prefixed to a chunk. It is kept
// short and is deliberately part of the stored text: it gives the answering
// model the reference it needs to cite a section, and gives the embedding a
// little topical anchoring.
//
// Any metadata key that is not a structural infrastructure key (book, heading,
// breadcrumb, content_type, noise_score) is rendered as a reference label.
// This makes contextHeader domain-neutral: whether the key is "section",
// "verse", "clause", or "dharana", it is rendered uniformly.
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
		parts = append(parts, truncateSegment(crumb))
	}
	// Render every user-defined reference key that isn't already in the breadcrumb.
	// Keys are sorted so the header string is deterministic across runs.
	var refKeys []string
	for k := range meta {
		if !structuralMetaKeys[k] && meta[k] != "" {
			refKeys = append(refKeys, k)
		}
	}
	sort.Strings(refKeys)

	for _, key := range refKeys {
		val := meta[key]
		// Use the first number only in the header label; full list is in metadata.
		num := strings.SplitN(val, ",", 2)[0]
		if num != "" && !anyHasNumber(parts, num) {
			// Capitalise the key for readability: "section" → "Section"
			label := strings.ToUpper(key[:1]) + key[1:]
			parts = append(parts, label+" "+num)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "[" + strings.Join(parts, " > ") + "]"
}

// maxHeaderSegment bounds one breadcrumb segment in a citation header.
//
// A heading is whatever follows the hashes on a line, so a document that
// numbers its passages by putting the whole passage in the heading yields a
// breadcrumb segment hundreds of characters long. That header is prefixed to
// the chunk text, embedded with it, and shown as the citation — so an uncapped
// one is unquotable and crowds out the passage it is meant to locate.
//
// The cap is on display only. Chunk metadata keeps the full heading, so
// nothing that reads MetaHeading or MetaBreadcrumb loses fidelity.
const maxHeaderSegment = 60

// truncateSegment shortens one breadcrumb segment at a word boundary where it
// can, marking the cut so a reader can see the heading was longer.
func truncateSegment(s string) string {
	if utf8.RuneCountInString(s) <= maxHeaderSegment {
		return s
	}
	runes := []rune(s)
	cut := maxHeaderSegment
	// Prefer the last space in the final quarter, so the label ends on a word.
	for i := cut - 1; i > maxHeaderSegment*3/4; i-- {
		if runes[i] == ' ' {
			cut = i
			break
		}
	}
	return strings.TrimRight(string(runes[:cut]), " ,;:-") + "…"
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
// header does not repeat "Section 4.2" after a heading that reads "4.2".
func anyHasNumber(parts []string, num string) bool {
	for _, p := range parts {
		for _, f := range strings.FieldsFunc(p, func(r rune) bool { return !unicode.IsDigit(r) && r != '.' }) {
			if f == num {
				return true
			}
		}
	}
	return false
}

// The context header used to be bounded by a ratio of the chunk size, applied
// as a clamp on the *deduction* rather than on the header. That inverted the
// guarantee it was written for: an oversized header was under-counted, so it
// rendered in full and the finished chunk ran past the size it was budgeted
// against. The header is now bounded at the source instead, one breadcrumb
// segment at a time — see maxHeaderSegment — and its real length is deducted.

// unit is one indivisible piece of text plus the section context it came from.
// Sections are turned into units first and packed into chunks second: emitting
// one chunk per section would fragment a verse-per-heading text into thousands
// of tiny chunks, roughly doubling embedding cost and splitting each verse from
// the commentary that explains it.
type unit struct {
	text       string
	breadcrumb string
	heading    string
	// refs holds the reference numbers extracted from this unit's heading,
	// keyed by meta_key. Used to decide whether to start a new chunk.
	refs map[string]string
	// carry is the leading text this unit repeats from the unit before it, and
	// follows reports whether that predecessor is the immediately preceding
	// unit. Together they let the packer drop the repetition when both land in
	// one chunk, while leaving it intact across a real chunk boundary.
	carry   string
	follows bool
	// budget is the content allowance for this unit's section — the chunk size
	// less the citation header that will be prefixed to it.
	budget int
}

// extractTableHeader returns the header rows (column names + separator) from
// a table body, plus a bool indicating whether the body starts without a
// header row (i.e., is a mid-table continuation).
func extractTableHeader(body string) (header string, startsPartial bool) {
	lines := strings.Split(body, "\n")
	var headerLines []string
	foundSep := false
	nonTableBefore := 0
	inTable := false

	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" {
			continue
		}
		if tableRowRe.MatchString(t) {
			inTable = true
			headerLines = append(headerLines, line)
			if tableSepRe.MatchString(t) {
				foundSep = true
				break
			}
			// If we see multiple table rows at the very start without a separator,
			// it's a partial table continuation.
			if len(headerLines) >= 2 && !foundSep && nonTableBefore == 0 {
				return "", true
			}
		} else {
			if inTable {
				break
			}
			nonTableBefore++
		}
	}
	if foundSep && len(headerLines) >= 2 {
		return strings.Join(headerLines, "\n"), false
	}
	if len(headerLines) > 0 && !foundSep && nonTableBefore == 0 {
		return "", true
	}
	return "", false
}

// SplitStructured chunks a document along its heading structure, attaching
// citation metadata to every chunk. The matchers slice controls which
// numbered-item patterns are recognised; build it from config.ChunkerConfig
// using CompileRefMatchers.
//
// Documents without headings degrade to the same scored line splitting, which
// is still a strict improvement on blind character splitting: it cuts at
// paragraph boundaries rather than mid-word.
func (c *Chunker) SplitStructured(text, source string, matchers []refMatcher) ([]Chunk, error) {
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

		// Reserve worst-case header space out of the budget before splitting,
		// so an annotated chunk cannot exceed the embedder's limit.
		headerLen := utf8.RuneCountInString(contextHeader(map[string]string{
			MetaBook: book, MetaBreadcrumb: breadcrumb,
		}))
		// Deduct what the header will actually cost, so body plus header fits
		// the chunk size. The estimate is a lower bound: reference labels
		// ("Verse 51") are appended at flush time, once the chunk's full
		// reference set is known. They are short and few.
		//
		// A chunk can still exceed ChunkSize in one case this cannot reach: a
		// single source line longer than the budget. splitScored cuts on line
		// boundaries, so a paragraph written as one unbroken line is
		// indivisible and is emitted whole.
		budget := c.opts.ChunkSize - headerLen - 2
		if budget < 200 {
			budget = 200
		}

		body := sec.body
		if sec.heading != "" {
			body = sec.heading + "\n\n" + body
		}

		// Extract first reference for each key from the heading.
		headRefs := map[string]string{}
		for _, m := range matchers {
			if hit := m.re.FindStringSubmatch(sec.heading); hit != nil {
				if _, ok := headRefs[m.metaKey]; !ok {
					headRefs[m.metaKey] = hit[1]
				}
			}
		}

		for i, piece := range splitScored(body, budget, c.opts.Overlap) {
			units = append(units, unit{
				text:       piece.text,
				breadcrumb: breadcrumb,
				heading:    sec.heading,
				refs:       headRefs,
				carry:      piece.carry,
				follows:    i > 0,
				budget:     budget,
			})
		}
	}

	var (
		chunks          []Chunk
		idx             int
		buf             []unit
		bufLen          int
		lastTableHeader string // carries table header across chunk boundaries
	)

	flush := func() {
		if len(buf) == 0 {
			return
		}
		texts := make([]string, 0, len(buf))
		for i, u := range buf {
			text := u.text
			// Consecutive pieces of a section overlap by design, so a passage
			// split across a chunk boundary reads whole in both. When both
			// pieces land in this same chunk there is no boundary to bridge,
			// and keeping the overlap would store the passage twice.
			//
			// buf is filled in order and never skips, so a unit at i > 0 whose
			// predecessor exists has that predecessor at i-1.
			if i > 0 && u.follows {
				text = stripCarry(text, u.carry)
			}
			if text != "" {
				texts = append(texts, text)
			}
		}
		body := strings.Join(texts, "\n\n")

		// Table header carry-forward (Issue 5 fix):
		// When a table chunk starts with a data row (no header), prepend the
		// last known header so the column context is not lost.
		ctype, noise := classify(body)
		if ctype == ContentTable {
			_, startsPartial := extractTableHeader(body)
			if startsPartial && lastTableHeader != "" {
				body = lastTableHeader + "\n" + body
			}
			// Update the stored header for the next chunk.
			if hdr, partial := extractTableHeader(body); !partial && hdr != "" {
				lastTableHeader = hdr
			}
		} else {
			lastTableHeader = ""
		}

		meta := map[string]string{
			MetaBook:       book,
			MetaHeading:    buf[0].heading,
			MetaBreadcrumb: buf[0].breadcrumb,
		}

		// Collect every reference number in the chunk, deduplicated.
		// One chunk can span multiple sections (e.g. Section 4.1 and 4.2).
		headings := make([]string, len(buf))
		for i, u := range buf {
			headings[i] = u.heading
		}
		allRefs := extractRefs(headings, body, matchers)
		for key, vals := range allRefs {
			meta[key] = strings.Join(vals, ",")
		}

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

		// Start a new chunk when this unit opens a numbered reference that
		// differs from what the buffer already covers, so that numbered items
		// remain individually addressable.
		if bufLen > 0 && startsNewReference(buf, u) {
			flush()
		} else if bufLen > 0 && bufLen+n+2 > buf[0].budget {
			// Pack to the section's content budget, not the raw chunk size.
			// The citation header is prefixed to the body at flush time, so
			// filling the buffer to ChunkSize and then adding the header put
			// the finished chunk over the embedder's limit — which is what the
			// per-piece budget was already computed to prevent. buf[0] is the
			// unit whose heading and breadcrumb become the header.
			flush()
		}

		buf = append(buf, u)
		bufLen += n + 2
	}
	flush()

	return chunks, nil
}

// startsNewReference reports whether u opens a numbered reference under any
// known meta key that differs from what the buffer already covers.
func startsNewReference(buf []unit, u unit) bool {
	for key, uVal := range u.refs {
		if uVal == "" {
			continue
		}
		for _, b := range buf {
			if bVal, ok := b.refs[key]; ok && bVal != "" && bVal != uVal {
				return true
			}
		}
	}
	return false
}

// MaxRefPatternLen bounds a reference pattern. Patterns run against every
// heading and body at build time and against every query at serve time, so an
// unbounded one is a performance hazard as well as an unreadable one.
const MaxRefPatternLen = 200

// RulesVersion identifies the chunking and tagging rules a corpus was built
// under. It is stored in the build manifest so an index built by an older
// binary can be recognised as stale.
//
// Bump it whenever a change alters the chunks produced from unchanged text —
// either which references are extracted, or the chunk text itself. The domain
// signature cannot cover either: it hashes the pattern strings from the config,
// so changes to how chunks are cut, joined or annotated are invisible to it.
//
//	1 — original.
//	2 — patterns compiled with (?m); see CompileRefPattern.
//	3 — overlap no longer duplicated within a chunk; chunks packed to the
//	    content budget rather than the raw chunk size; header segments capped.
const RulesVersion = 3

// multilineFlag makes a leading ^ mean start-of-line.
//
// This is load-bearing, not cosmetic. Reference patterns are matched against
// whole chunk bodies (see extractRefs), which are multi-line strings. Without
// this flag a pattern like `^\s*(\d{1,4})\)` matches only when the body itself
// begins with the marker, so every later occurrence in the same chunk is
// dropped — the marker is present in the text, and simply never tagged.
//
// Go's regexp accepts stacked flag groups, so prefixing a pattern that already
// carries its own — `(?i)…` becoming `(?m)(?i)…` — is well defined.
const multilineFlag = "(?m)"

// CompileRefPattern compiles one reference pattern for matching against
// multi-line text, applying the validation every caller needs.
//
// Both call sites must go through this. The chunker compiles patterns for
// chunk bodies and the retrieval layer compiles the same patterns for queries;
// queries are single-line, so a missing (?m) is harmless there and silently
// destructive here. Compiling in one place is what keeps the two consistent.
//
// The capture-group check is not cosmetic: a pattern with no capture group
// makes FindAllStringSubmatch return rows of length one, and both extractRefs
// and queryRefs then index hit[1] — an index-out-of-range panic.
//
// Note that (?m) also makes $ line-anchored.
func CompileRefPattern(p config.RefPattern) (*regexp.Regexp, error) {
	if p.Pattern == "" || p.MetaKey == "" {
		return nil, fmt.Errorf("ref_pattern needs both a pattern and a meta_key")
	}
	if len(p.Pattern) > MaxRefPatternLen {
		return nil, fmt.Errorf(
			"ref_pattern for %q: %d characters exceeds the %d-character limit",
			p.MetaKey, len(p.Pattern), MaxRefPatternLen)
	}
	re, err := regexp.Compile(multilineFlag + p.Pattern)
	if err != nil {
		// Report the pattern as the user wrote it, not as we compiled it.
		return nil, fmt.Errorf("invalid ref_pattern %q: %w", p.Pattern, err)
	}
	if n := re.NumSubexp(); n != 1 {
		return nil, fmt.Errorf(
			"ref_pattern %q: needs exactly one capture group for the number, found %d",
			p.Pattern, n)
	}
	return re, nil
}

// CompileRefMatchersVerbose compiles reference patterns and reports why any
// were rejected, so a bad pattern is visible rather than silently inert.
//
// Every rejection reason here is load-bearing. A pattern with no capture group
// makes FindAllStringSubmatch return rows of length one, and extractRefs then
// indexes hit[1] — an index-out-of-range panic. That was reachable only through
// a hand-written agent.yaml typo before; once patterns are generated it becomes
// a crash that can ship.
func CompileRefMatchersVerbose(patterns []config.RefPattern) ([]refMatcher, []string) {
	out := make([]refMatcher, 0, len(patterns))
	var warnings []string

	for _, p := range patterns {
		// A wholly empty entry is an unfilled template line, not a mistake
		// worth reporting.
		if p.Pattern == "" || p.MetaKey == "" {
			continue
		}
		re, err := CompileRefPattern(p)
		if err != nil {
			warnings = append(warnings, "skipping "+err.Error())
			continue
		}
		out = append(out, refMatcher{re: re, metaKey: p.MetaKey})
	}
	return out, warnings
}

// DocumentHeadings returns every heading in a document, outermost first, in
// document order.
//
// Exposed for corpus profiling, which scores candidate numbering patterns
// against headings. It reuses parseSections rather than re-implementing the
// fence and level handling, so a profiler and the chunker always agree on what
// counts as a heading.
func DocumentHeadings(text string) []string {
	sections := parseSections(strings.ReplaceAll(text, "\r\n", "\n"))
	out := make([]string, 0, len(sections))
	for _, s := range sections {
		if h := strings.TrimSpace(s.heading); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// BookTitle derives a human-readable work title from a source filename.
// Exposed so corpus profiling tokenizes titles exactly as work-grouping does.
func BookTitle(source string) string { return bookTitle(source) }
