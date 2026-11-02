package checker

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// blockKind classifies one Markdown block. Fenced code never reaches this
// representation: code is removed while parsing and only counted.
type blockKind int

const (
	blockProse blockKind = iota
	blockHeading
	blockList
	blockQuote
	blockTable
)

// block is one blank-line separated chunk of the answer.
type block struct {
	kind   blockKind
	lines  []string // trailing whitespace trimmed, original indentation kept
	paraID int      // 1-based natural-paragraph number; 0 for headings/tables
}

// document is the parsed, analysis-ready form of an answer.
type document struct {
	blocks []block
	stats  Stats
}

// splitLines normalises line endings and splits text into lines.
func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.Split(text, "\n")
}

// parseDocument removes fenced code blocks, groups the remaining lines into
// Markdown-ish blocks and computes the cheap statistics.
func parseDocument(text string) *document {
	doc := &document{stats: Stats{Runes: utf8.RuneCountInString(text)}}

	var (
		cur     []string
		curKind blockKind
		fence   string
		inFence bool
	)
	flush := func() {
		if len(cur) == 0 {
			return
		}
		kind := curKind
		if kind == blockTable && len(cur) < 2 {
			// A lone pipe-ended line is prose, not a table.
			kind = blockProse
		}
		doc.blocks = append(doc.blocks, block{kind: kind, lines: cur})
		cur = nil
		curKind = blockProse
	}

	for _, raw := range splitLines(text) {
		line := strings.TrimRight(raw, " \t")
		trimmed := strings.TrimSpace(line)

		if inFence {
			if strings.HasPrefix(trimmed, fence) {
				inFence = false
				fence = ""
			}
			continue
		}
		if isFenceLine(trimmed) {
			flush()
			inFence = true
			fence = trimmed[:3]
			doc.stats.CodeBlocks++
			continue
		}
		if trimmed == "" {
			flush()
			continue
		}
		if isThematicBreak(trimmed) {
			flush()
			continue
		}
		if isHeadingLine(trimmed) {
			flush()
			doc.blocks = append(doc.blocks, block{kind: blockHeading, lines: []string{line}})
			continue
		}

		kind := lineKind(trimmed)
		if curKind == blockList && kind != blockList && isIndentedLine(line) {
			// Indented continuation of the previous list item.
			cur = append(cur, line)
			continue
		}
		if len(cur) > 0 && kind != curKind {
			flush()
		}
		if len(cur) == 0 {
			curKind = kind
		}
		cur = append(cur, line)
	}
	flush()

	doc.blocks = mergeAdjacentListBlocks(doc.blocks)
	doc.assignParagraphIDs()

	doc.stats.Paragraphs = doc.paragraphCount()
	doc.stats.Sentences = doc.sentenceCount()

	var natural []string
	for i := range doc.blocks {
		if doc.blocks[i].kind == blockTable {
			continue // table markup is not natural-language prose
		}
		natural = append(natural, doc.blocks[i].lines...)
	}
	doc.stats.NaturalRunes = utf8.RuneCountInString(strings.Join(natural, "\n"))
	return doc
}

func (d *document) assignParagraphIDs() {
	n := 0
	for i := range d.blocks {
		if isNaturalKind(d.blocks[i].kind) {
			n++
			d.blocks[i].paraID = n
		}
	}
}

func (d *document) paragraphCount() int {
	n := 0
	for i := range d.blocks {
		if isNaturalKind(d.blocks[i].kind) {
			n++
		}
	}
	return n
}

func (d *document) sentenceCount() int {
	n := 0
	for i := range d.blocks {
		if !isNaturalKind(d.blocks[i].kind) {
			continue
		}
		n += len(sentencesOf(&d.blocks[i]))
	}
	return n
}

func isNaturalKind(k blockKind) bool {
	return k == blockProse || k == blockQuote || k == blockList
}

// mergeAdjacentListBlocks joins loose lists (items separated by blank lines)
// so they are counted and reported as one list.
func mergeAdjacentListBlocks(blocks []block) []block {
	out := blocks[:0]
	for i := range blocks {
		b := blocks[i]
		if len(out) > 0 && out[len(out)-1].kind == blockList && b.kind == blockList {
			out[len(out)-1].lines = append(out[len(out)-1].lines, b.lines...)
			continue
		}
		out = append(out, b)
	}
	return out
}

// blockPlainText renders a natural block as text with block markup removed.
func blockPlainText(b *block) string {
	if b.kind == blockList {
		return strings.Join(listItems(b), "\n")
	}
	lines := make([]string, 0, len(b.lines))
	for _, line := range b.lines {
		lines = append(lines, stripBlockPrefix(line))
	}
	return strings.Join(lines, "\n")
}

// sentencesOf returns the raw sentences of one natural block.
func sentencesOf(b *block) []string {
	switch b.kind {
	case blockList:
		var out []string
		for _, item := range listItems(b) {
			out = append(out, splitSentences(item)...)
		}
		return out
	case blockProse, blockQuote:
		lines := make([]string, 0, len(b.lines))
		for _, line := range b.lines {
			lines = append(lines, stripBlockPrefix(line))
		}
		return splitSentences(strings.Join(lines, " "))
	default:
		return nil
	}
}

// listItems splits a list block into items, joining continuation lines.
func listItems(b *block) []string {
	var items []string
	for _, line := range b.lines {
		trimmed := strings.TrimSpace(line)
		if isListItemLine(trimmed) {
			items = append(items, strings.TrimSpace(trimmed[listMarkerBytes(trimmed):]))
			continue
		}
		if len(items) > 0 {
			items[len(items)-1] = strings.TrimSpace(items[len(items)-1] + " " + trimmed)
			continue
		}
		items = append(items, trimmed)
	}
	return items
}

// isFenceLine reports whether a line opens a fenced code block.
func isFenceLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "\u0060\u0060\u0060") || strings.HasPrefix(trimmed, "~~~")
}

// isHeadingLine reports whether a line is an ATX heading.
func isHeadingLine(trimmed string) bool {
	i := 0
	for i < len(trimmed) && trimmed[i] == '#' {
		i++
	}
	if i == 0 || i > 6 {
		return false
	}
	return i == len(trimmed) || trimmed[i] == ' ' || trimmed[i] == '\t'
}

// isThematicBreak reports whether a line is a horizontal rule.
func isThematicBreak(trimmed string) bool {
	if len(trimmed) < 3 {
		return false
	}
	n := 0
	for _, r := range trimmed {
		switch r {
		case '-', '*', '_':
			n++
		case ' ', '\t':
		default:
			return false
		}
	}
	return n >= 3
}

// lineKind classifies one non-empty, non-code, non-heading line.
func lineKind(trimmed string) blockKind {
	switch {
	case strings.HasPrefix(trimmed, ">"):
		return blockQuote
	case strings.HasPrefix(trimmed, "|"), strings.HasSuffix(trimmed, "|"):
		return blockTable
	case isListItemLine(trimmed):
		return blockList
	default:
		return blockProse
	}
}

func isIndentedLine(line string) bool {
	return line != "" && (line[0] == ' ' || line[0] == '\t')
}

// isListItemLine reports whether a line starts with a Markdown list marker.
func isListItemLine(s string) bool { return listMarkerBytes(s) > 0 }

// listMarkerBytes returns the byte length of the list marker plus its trailing
// space, or 0 when the line is not a list item.
func listMarkerBytes(s string) int {
	if len(s) >= 2 && s[1] == ' ' {
		switch s[0] {
		case '-', '*', '+':
			return 2
		}
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(s) && (s[i] == '.' || s[i] == ')') && s[i+1] == ' ' {
		return i + 2
	}
	return 0
}

// stripBlockPrefix removes quote/heading/list markers from one line.
func stripBlockPrefix(line string) string {
	s := strings.TrimSpace(line)
	for {
		switch {
		case strings.HasPrefix(s, ">"):
			s = strings.TrimSpace(s[1:])
		case isHeadingLine(s):
			i := 0
			for i < len(s) && s[i] == '#' {
				i++
			}
			s = strings.TrimSpace(s[i:])
		case isListItemLine(s):
			s = strings.TrimSpace(s[listMarkerBytes(s):])
		default:
			return s
		}
	}
}

// stripInlineMarkdown keeps visible text and drops emphasis, inline-code
// markers and link/image destinations. It deliberately does not recurse.
func stripInlineMarkdown(s string) string {
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(runes); {
		r := runes[i]
		switch {
		case r == '\\' && i+1 < len(runes):
			b.WriteRune(runes[i+1])
			i += 2
		case r == '!' && i+1 < len(runes) && runes[i+1] == '[':
			if end, next, ok := linkSpan(runes, i+1); ok {
				b.WriteString(string(runes[i+2 : end]))
				i = next
			} else {
				i++
			}
		case r == '[':
			if end, next, ok := linkSpan(runes, i); ok {
				b.WriteString(string(runes[i+1 : end]))
				i = next
			} else {
				b.WriteRune(r)
				i++
			}
		case r == '\u0060' || r == '*' || r == '~' || r == '_':
			i++
		default:
			b.WriteRune(r)
			i++
		}
	}
	return b.String()
}

// linkSpan locates [text](url) / [text][ref] and returns the closing bracket
// index of the visible text and the index right after the whole link.
func linkSpan(runes []rune, open int) (int, int, bool) {
	limit := open + maxInlineSpanRunes
	if limit > len(runes) {
		limit = len(runes)
	}
	closeBracket := -1
	for i := open + 1; i < limit; i++ {
		switch runes[i] {
		case ']':
			closeBracket = i
		case '[', '\n':
			return 0, 0, false
		}
		if closeBracket >= 0 {
			break
		}
	}
	if closeBracket < 0 || closeBracket+1 >= len(runes) {
		return 0, 0, false
	}
	var closer rune
	switch runes[closeBracket+1] {
	case '(':
		closer = ')'
	case '[':
		closer = ']'
	default:
		return 0, 0, false
	}
	endLimit := closeBracket + 2 + maxInlineSpanRunes
	if endLimit > len(runes) {
		endLimit = len(runes)
	}
	for i := closeBracket + 2; i < endLimit; i++ {
		if runes[i] == closer {
			return closeBracket, i + 1, true
		}
		if runes[i] == '\n' {
			return 0, 0, false
		}
	}
	return 0, 0, false
}

// normalize produces the comparison form used by both duplicate rules:
// markdown stripped, fullwidth punctuation folded to ASCII, ASCII lowercased,
// whitespace collapsed and spaces between CJK characters removed.
func normalize(s string) string {
	lines := splitLines(s)
	parts := make([]string, 0, len(lines))
	for _, line := range lines {
		line = stripBlockPrefix(line)
		line = stripInlineMarkdown(line)
		if strings.TrimSpace(line) != "" {
			parts = append(parts, line)
		}
	}
	return normalizeRunes(strings.Join(parts, " "))
}

func normalizeRunes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\ufeff':
			continue
		case '\u3001', '\uff0c': // 、 ，
			b.WriteRune(',')
		case '\u3002': // 。
			b.WriteRune('.')
		case '\uff01': // ！
			b.WriteRune('!')
		case '\uff1f': // ？
			b.WriteRune('?')
		case '\uff1b': // ；
			b.WriteRune(';')
		case '\uff1a': // ：
			b.WriteRune(':')
		case '\uff08': // （
			b.WriteRune('(')
		case '\uff09': // ）
			b.WriteRune(')')
		case '\u3010': // 【
			b.WriteRune('[')
		case '\u3011': // 】
			b.WriteRune(']')
		case '\u300c', '\u300d', '\u300e', '\u300f', '\u201c', '\u201d', '\u201e': // quotes
			b.WriteRune('"')
		case '\u2018', '\u2019':
			b.WriteRune('\'')
		case '\u2014', '\u2013', '\u2212': // dashes
			b.WriteRune('-')
		case '\u2026': // ellipsis
			b.WriteString("...")
		case '\uff5e', '\u301c':
			b.WriteRune('~')
		case '\u300a': // 《
			b.WriteRune('<')
		case '\u300b': // 》
			b.WriteRune('>')
		case '\u3000': // ideographic space
			b.WriteRune(' ')
		default:
			if r >= '\uff01' && r <= '\uff5e' {
				r -= 0xFEE0
			}
			if r >= 'A' && r <= 'Z' {
				r += 'a' - 'A'
			}
			b.WriteRune(r)
		}
	}
	return removeCJKSpace(collapseWhitespace(b.String()))
}

// collapseWhitespace folds every whitespace run to one space and trims.
func collapseWhitespace(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	pendingSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if b.Len() > 0 {
				pendingSpace = true
			}
			continue
		}
		if pendingSpace {
			b.WriteRune(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
	}
	return b.String()
}

// removeCJKSpace drops spaces that are pure typographic noise: between two
// CJK characters, and between CJK text and adjacent ASCII punctuation. Latin
// words keep their separating spaces.
func removeCJKSpace(s string) string {
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	for i, r := range runes {
		if r == ' ' && i > 0 && i+1 < len(runes) {
			prev, next := runes[i-1], runes[i+1]
			cjkPrev, cjkNext := isCJK(prev), isCJK(next)
			punctPrev, punctNext := unicode.IsPunct(prev) || unicode.IsSymbol(prev),
				unicode.IsPunct(next) || unicode.IsSymbol(next)
			if cjkNext && (cjkPrev || punctPrev) {
				continue
			}
			if cjkPrev && (punctNext || cjkNext) {
				continue
			}
		}
		out = append(out, r)
	}
	return string(out)
}

// isCJK reports whether r belongs to a CJK script block.
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK unified ideographs
		(r >= 0x3400 && r <= 0x4DBF) || // CJK extension A
		(r >= 0xF900 && r <= 0xFAFF) || // CJK compatibility ideographs
		(r >= 0x3040 && r <= 0x30FF) || // kana
		(r >= 0xAC00 && r <= 0xD7AF) // Hangul
}

func countCJK(s string) int {
	n := 0
	for _, r := range s {
		if isCJK(r) {
			n++
		}
	}
	return n
}

func hasCJK(s string) bool { return countCJK(s) > 0 }

// hasTextRune reports whether s contains at least one letter or digit.
func hasTextRune(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

func containsDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

func containsBacktick(s string) bool { return strings.ContainsRune(s, '\u0060') }

func containsURL(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "http://") || strings.Contains(lower, "https://")
}

// evidenceFrom flattens and truncates a text snippet for Finding.Evidence.
func evidenceFrom(text string) string {
	flat := collapseWhitespace(text)
	runes := []rune(flat)
	if len(runes) <= maxEvidenceRunes {
		return flat
	}
	return string(runes[:maxEvidenceRunes])
}

// splitSentences splits text on Chinese and English sentence punctuation.
// Paragraph and hard line breaks are boundaries too.
func splitSentences(text string) []string {
	runes := []rune(text)
	var out []string
	start := 0
	flush := func(end int) {
		if end <= start {
			return
		}
		s := strings.TrimSpace(string(runes[start:end]))
		if s != "" {
			out = append(out, s)
		}
		start = end
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '\n', '\r':
			flush(i + 1)
		case '\u3002', '\uff01', '\uff1f', '\uff1b', '\u2026', '!', '?', ';':
			j := i + 1
			for j < len(runes) && (isSentenceTerminator(runes[j]) || isSentenceCloser(runes[j])) {
				j++
			}
			flush(j)
			i = j - 1
		case '.':
			if !isSentenceDot(runes, i) {
				continue
			}
			j := i + 1
			for j < len(runes) && isSentenceCloser(runes[j]) {
				j++
			}
			flush(j)
			i = j - 1
		}
	}
	flush(len(runes))
	return out
}

func isSentenceTerminator(r rune) bool {
	switch r {
	case '\u3002', '\uff01', '\uff1f', '\uff1b', '\u2026', '!', '?', ';':
		return true
	}
	return false
}

func isSentenceCloser(r rune) bool {
	switch r {
	case '\u300d', '\u300f', '\u201d', '\u2019', ')', ']', '\u3011', '\u300b', '\u3009':
		return true
	}
	return false
}

// isSentenceDot reports whether the '.' at index i ends a sentence, as opposed
// to a decimal point, a filename/identifier dot or a known abbreviation.
func isSentenceDot(runes []rune, i int) bool {
	if i > 0 && runes[i-1] == '.' {
		return false
	}
	if i+1 < len(runes) {
		next := runes[i+1]
		if unicode.IsDigit(next) && i > 0 && unicode.IsDigit(runes[i-1]) {
			return false
		}
		if unicode.IsLetter(next) {
			return false
		}
	}
	return !isAbbreviationDot(runes[:i])
}

func isAbbreviationDot(prefix []rune) bool {
	j := len(prefix) - 1
	for j >= 0 && unicode.IsSpace(prefix[j]) {
		j--
	}
	end := j + 1
	for j >= 0 {
		r := prefix[j]
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			j--
			continue
		}
		break
	}
	word := strings.ToLower(string(prefix[j+1 : end]))
	if word == "" {
		return false
	}
	if len([]rune(word)) == 1 {
		return true
	}
	switch word {
	case "e.g", "i.e", "etc", "vs", "mr", "mrs", "ms", "dr", "prof", "st", "inc", "ltd", "co", "jr", "sr", "fig", "no", "approx", "dept":
		return true
	}
	return false
}
