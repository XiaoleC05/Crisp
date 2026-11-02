package checker

import (
	"fmt"
	"unicode/utf8"
)

// shingleSet is a set of hashed character shingles.
type shingleSet map[uint64]struct{}

// shingleSize uses bigrams for CJK text and trigrams for Latin text.
func shingleSize(cjk bool) int {
	if cjk {
		return 2
	}
	return 3
}

// makeShingles hashes every character shingle of norm.
func makeShingles(norm string, cjk bool) shingleSet {
	runes := []rune(norm)
	k := shingleSize(cjk)
	if len(runes) <= k {
		return shingleSet{hashRunes(runes): struct{}{}}
	}
	set := make(shingleSet, len(runes)-k+1)
	for i := 0; i+k <= len(runes); i++ {
		set[hashRunes(runes[i:i+k])] = struct{}{}
	}
	return set
}

// hashRunes is a small FNV-1a hash over runes; collisions are harmless for a
// similarity pre-check because the exact-normalisation equality check is
// separate.
func hashRunes(runes []rune) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	for _, r := range runes {
		h ^= uint64(r)
		h *= prime64
	}
	return h
}

// jaccard returns the Jaccard similarity of two shingle sets.
func jaccard(a, b shingleSet) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	inter := 0
	for h := range a {
		if _, ok := b[h]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// lengthCompatible is a cheap pre-filter: two shingle sets with Jaccard >= J
// cannot differ in size by more than a factor of 1/J.
func lengthCompatible(a, b int) bool {
	if a <= 0 || b <= 0 {
		return false
	}
	if a > b {
		a, b = b, a
	}
	return float64(b) <= float64(a)/duplicateSimilarity
}

// paragraphLocation renders the stable location label used by all rules.
func paragraphLocation(paraID int) string {
	return fmt.Sprintf("paragraph_%d", paraID)
}

// paragraphInfo is one analysed natural block.
type paragraphInfo struct {
	block    *block
	norm     string
	runes    int
	cjk      bool
	shingles shingleSet
	display  string
}

// collectParagraphs normalises every natural paragraph that is long enough to
// carry a duplicated idea. Tables, headings and code are intentionally absent.
func collectParagraphs(doc *document) []paragraphInfo {
	paras := make([]paragraphInfo, 0, len(doc.blocks))
	for i := range doc.blocks {
		b := &doc.blocks[i]
		if !isNaturalKind(b.kind) {
			continue
		}
		plain := blockPlainText(b)
		norm := normalize(plain)
		runes := utf8.RuneCountInString(norm)
		if runes < minParagraphRunes || !hasTextRune(norm) {
			continue
		}
		cjk := hasCJK(norm)
		paras = append(paras, paragraphInfo{
			block:    b,
			norm:     norm,
			runes:    runes,
			cjk:      cjk,
			shingles: makeShingles(norm, cjk),
			display:  evidenceFrom(plain),
		})
		if len(paras) >= maxAnalyzedParagraphs {
			break
		}
	}
	return paras
}

// duplicateParagraphFindings reports exact and high-similarity paragraph
// duplicates. Both severities are material: a duplicated paragraph is a strong
// hint, but only the critic may turn it into a block.
func duplicateParagraphFindings(doc *document) []Finding {
	paras := collectParagraphs(doc)
	if len(paras) < 2 {
		return nil
	}
	findings := make([]Finding, 0, 2)
	reported := make([]bool, len(paras))

	firstByNorm := make(map[string]int, len(paras))
	for j := range paras {
		if i, ok := firstByNorm[paras[j].norm]; ok {
			reported[j] = true
			findings = append(findings, Finding{
				Type:     FindingDuplicateParagraph,
				Severity: SeverityMaterial,
				Location: paragraphLocation(paras[j].block.paraID),
				Evidence: paras[j].display,
				Detail:   fmt.Sprintf("exact duplicate of paragraph_%d after normalisation", paras[i].block.paraID),
			})
			if len(findings) >= maxFindingsPerType {
				return findings
			}
			continue
		}
		firstByNorm[paras[j].norm] = j
	}
	if len(findings) >= maxFindingsPerType {
		return findings
	}

	for j := range paras {
		if reported[j] || paras[j].runes < nearDuplicateParagraphRunes {
			continue
		}
		best := -1
		bestSim := 0.0
		for i := 0; i < j; i++ {
			if paras[i].runes < nearDuplicateParagraphRunes || paras[i].cjk != paras[j].cjk {
				continue
			}
			if !lengthCompatible(paras[i].runes, paras[j].runes) {
				continue
			}
			if sim := jaccard(paras[i].shingles, paras[j].shingles); sim > bestSim {
				best, bestSim = i, sim
			}
		}
		if best < 0 || bestSim < duplicateSimilarity {
			continue
		}
		findings = append(findings, Finding{
			Type:     FindingDuplicateParagraph,
			Severity: SeverityMaterial,
			Location: paragraphLocation(paras[j].block.paraID),
			Evidence: paras[j].display,
			Detail:   fmt.Sprintf("near-duplicate (similarity %.2f) of paragraph_%d", bestSim, paras[best].block.paraID),
		})
		if len(findings) >= maxFindingsPerType {
			break
		}
	}
	return findings
}

// sentenceInfo is one analysed sentence.
type sentenceInfo struct {
	norm     string
	runes    int
	cjk      bool
	shingles shingleSet
	display  string
	paraID   int
}

// collectSentences normalises every sentence long enough to count as a
// duplicated thought. Code blocks and tables never reach it.
func collectSentences(doc *document) []sentenceInfo {
	sentences := make([]sentenceInfo, 0, 64)
	for i := range doc.blocks {
		b := &doc.blocks[i]
		if !isNaturalKind(b.kind) {
			continue
		}
		for _, raw := range sentencesOf(b) {
			norm := normalize(raw)
			if !hasTextRune(norm) {
				continue
			}
			runes := utf8.RuneCountInString(norm)
			if runes < minSentenceRunes(hasCJK(norm)) {
				continue
			}
			cjk := hasCJK(norm)
			sentences = append(sentences, sentenceInfo{
				norm:     norm,
				runes:    runes,
				cjk:      cjk,
				shingles: makeShingles(norm, cjk),
				display:  evidenceFrom(raw),
				paraID:   b.paraID,
			})
			if len(sentences) >= maxAnalyzedSentences {
				return sentences
			}
		}
	}
	return sentences
}

// minSentenceRunes returns the minimum length a sentence needs before it is
// treated as duplicated content.
func minSentenceRunes(cjk bool) int {
	if cjk {
		return minSentenceCJKRunes
	}
	return minSentenceLatinRunes
}

// sentenceMaterialRunes returns the minimum length of a strongly duplicated
// sentence for it to be reported as material.
func sentenceMaterialRunes(cjk bool) int {
	if cjk {
		return materialSentenceCJKRunes
	}
	return materialSentenceLatinRunes
}

// duplicateSentenceFindings reports exact and high-similarity sentence
// duplicates across the whole answer.
func duplicateSentenceFindings(doc *document) []Finding {
	sentences := collectSentences(doc)
	if len(sentences) < 2 {
		return nil
	}
	findings := make([]Finding, 0, 2)
	reported := make([]bool, len(sentences))

	firstByNorm := make(map[string]int, len(sentences))
	for j := range sentences {
		if i, ok := firstByNorm[sentences[j].norm]; ok {
			reported[j] = true
			findings = append(findings, sentenceFinding(sentences[j], sentences[i], 1, true))
			if len(findings) >= maxFindingsPerType {
				return findings
			}
			continue
		}
		firstByNorm[sentences[j].norm] = j
	}
	if len(findings) >= maxFindingsPerType {
		return findings
	}

	for j := range sentences {
		if reported[j] {
			continue
		}
		best := -1
		bestSim := 0.0
		for i := 0; i < j; i++ {
			if sentences[i].cjk != sentences[j].cjk {
				continue
			}
			if !lengthCompatible(sentences[i].runes, sentences[j].runes) {
				continue
			}
			if sim := jaccard(sentences[i].shingles, sentences[j].shingles); sim > bestSim {
				best, bestSim = i, sim
			}
		}
		if best < 0 || bestSim < duplicateSimilarity {
			continue
		}
		findings = append(findings, sentenceFinding(sentences[j], sentences[best], bestSim, false))
		if len(findings) >= maxFindingsPerType {
			break
		}
	}
	return findings
}

// sentenceFinding builds one duplicate_sentence finding.
func sentenceFinding(cur, prev sentenceInfo, sim float64, exact bool) Finding {
	severity := SeverityMinor
	if sim >= sentenceMaterialSimilarity && cur.runes >= sentenceMaterialRunes(cur.cjk) {
		severity = SeverityMaterial
	}
	detail := fmt.Sprintf("near-duplicate (similarity %.2f) of a sentence in paragraph_%d", sim, prev.paraID)
	if exact {
		detail = fmt.Sprintf("exact duplicate of a sentence in paragraph_%d", prev.paraID)
	}
	return Finding{
		Type:     FindingDuplicateSentence,
		Severity: severity,
		Location: paragraphLocation(cur.paraID),
		Evidence: cur.display,
		Detail:   detail,
	}
}
