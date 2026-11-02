package checker

import (
	"fmt"
	"strings"
)

// structureFindings emits auxiliar risk signals about over-fragmented answers.
// They are never material: structure alone cannot justify a block.
func structureFindings(doc *document) []Finding {
	findings := make([]Finding, 0, 2)
	if finding, ok := tinyListItemsFinding(doc); ok {
		findings = append(findings, finding)
	}
	if finding, ok := fragmentedParagraphsFinding(doc); ok {
		findings = append(findings, finding)
	}
	if finding, ok := scaffoldListFinding(doc); ok {
		findings = append(findings, finding)
	}
	return findings
}

// tinyListItemsFinding flags lists that are mostly very short items, the
// typical "one bullet per word" scaffold.
func tinyListItemsFinding(doc *document) (Finding, bool) {
	total, tiny := 0, 0
	firstTiny := ""
	firstParaID := 0
	for i := range doc.blocks {
		b := &doc.blocks[i]
		if b.kind != blockList {
			continue
		}
		for _, item := range listItems(b) {
			norm := normalize(item)
			if !hasTextRune(norm) {
				continue
			}
			total++
			if runeCount(norm) >= tinyListItemRunes {
				continue
			}
			tiny++
			if firstTiny == "" {
				firstTiny = item
				firstParaID = b.paraID
			}
		}
	}
	if total < tinyListItemMinCount || tiny < tinyListItemMinCount {
		return Finding{}, false
	}
	if float64(tiny)/float64(total) < tinyListItemRatio {
		return Finding{}, false
	}
	return Finding{
		Type:     FindingStructureAnomaly,
		Severity: SeverityMinor,
		Location: paragraphLocation(firstParaID),
		Evidence: evidenceFrom(firstTiny),
		Detail:   fmt.Sprintf("%d of %d list items are shorter than %d runes", tiny, total, tinyListItemRunes),
	}, true
}

// fragmentedParagraphsFinding flags answers that split one thought into many
// one-sentence paragraphs.
func fragmentedParagraphsFinding(doc *document) (Finding, bool) {
	proseCount, shortSingle := 0, 0
	firstShort := ""
	firstParaID := 0
	for i := range doc.blocks {
		b := &doc.blocks[i]
		if b.kind != blockProse {
			continue
		}
		plain := strings.TrimSpace(blockPlainText(b))
		if plain == "" {
			continue
		}
		proseCount++
		if len(sentencesOf(b)) != 1 || runeCount(plain) > shortParagraphRunes {
			continue
		}
		shortSingle++
		if firstShort == "" {
			firstShort = plain
			firstParaID = b.paraID
		}
	}
	if proseCount < singleSentenceMinCount || shortSingle < singleSentenceMinCount {
		return Finding{}, false
	}
	if float64(shortSingle)/float64(proseCount) < singleSentenceRatio {
		return Finding{}, false
	}
	return Finding{
		Type:     FindingStructureAnomaly,
		Severity: SeverityMinor,
		Location: paragraphLocation(firstParaID),
		Evidence: evidenceFrom(firstShort),
		Detail:   fmt.Sprintf("%d of %d prose paragraphs are one short sentence", shortSingle, proseCount),
	}, true
}

// scaffoldListFinding flags a list whose items all repeat the same opening
// scaffold: one thought padded into many near-identical bullets. It is info
// only because normal checklists can share a verb.
func scaffoldListFinding(doc *document) (Finding, bool) {
	for i := range doc.blocks {
		b := &doc.blocks[i]
		if b.kind != blockList {
			continue
		}
		items := make([]string, 0, 8)
		for _, item := range listItems(b) {
			norm := normalize(item)
			if !hasTextRune(norm) {
				continue
			}
			items = append(items, norm)
		}
		if len(items) < scaffoldMinItems {
			continue
		}
		counts := make(map[string]int, len(items))
		for _, norm := range items {
			runes := []rune(norm)
			prefix := runes
			if len(runes) > scaffoldPrefixRunes {
				prefix = runes[:scaffoldPrefixRunes]
			}
			counts[string(prefix)]++
		}
		repeated := 0
		for _, n := range counts {
			if n > repeated {
				repeated = n
			}
		}
		if float64(repeated)/float64(len(items)) < scaffoldRatio {
			continue
		}
		return Finding{
			Type:     FindingStructureAnomaly,
			Severity: SeverityInfo,
			Location: paragraphLocation(b.paraID),
			Evidence: evidenceFrom(items[0]),
			Detail:   fmt.Sprintf("%d of %d list items repeat the same opening scaffold", repeated, len(items)),
		}, true
	}
	return Finding{}, false
}

// lengthFindings emits a purely descriptive risk signal for very long answers.
// Length is never a block reason by itself, so the severity is info only.
func lengthFindings(doc *document) []Finding {
	if doc.stats.NaturalRunes < longAnswerNaturalRunes {
		return nil
	}
	return []Finding{{
		Type:     FindingLengthSignal,
		Severity: SeverityInfo,
		Location: "document",
		Detail: fmt.Sprintf("long answer: %d natural-language runes; length alone is never a block reason",
			doc.stats.NaturalRunes),
	}}
}
