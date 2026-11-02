package checker

import (
	"fmt"
)

// Curated filler phrases. They are matched against normalised paragraph text,
// so entries are lowercase and free of punctuation variants.
//
// Keep the lists narrow enough that substantive openings ("下面给出配置")
// produce at most a minor hint and never a material one.
var fillerOpeningPhrases = []string{
	// Chinese opening filler.
	"好的", "当然可以", "当然", "没问题", "很高兴", "很乐意", "乐意",
	"感谢你", "感谢您的提问", "感谢你的提问", "谢谢你的提问", "谢谢你的问题",
	"这是个好问题", "这是一个好问题", "这是一个很好的问题", "这个问题问得",
	"你问的是", "关于你的问题", "针对你的问题", "根据你的描述", "根据你的问题",
	"在开始之前", "在回答之前", "在动手之前", "首先", "让我们",
	"下面我们", "接下来我们", "下面", "接下来", "众所周知", "随着",
	"简单来说", "简而言之", "总的来说", "总而言之",
	"需要注意的是", "需要说明的是", "值得注意的是", "值得一提的是",
	"我先", "我来帮你", "帮你分析", "我理解你", "明白你的意思",
	// English opening filler.
	"sure", "certainly", "of course", "absolutely",
	"great question", "good question", "that's a great question", "that is a great question",
	"happy to help", "i'd be happy", "i am happy", "i'd be glad", "i can help", "i will help",
	"let me", "let's", "let us", "here's", "here is",
	"in this answer", "in this article", "as you know", "as we all know",
	"it's worth noting", "it is worth noting", "first of all",
	"to answer your question", "to get started", "basically", "overall",
	"in summary", "to sum up", "thanks for asking", "thank you for asking",
}

var fillerClosingPhrases = []string{
	// Chinese closing filler.
	"希望以上内容对你有帮助", "希望以上内容能帮到你", "希望以上内容有所帮助",
	"希望这些内容对你有帮助", "希望这些内容能帮到你", "希望这些内容有所帮助",
	"希望对你有帮助", "希望对您有帮助", "希望对你有所帮助", "希望能帮到你",
	"希望有所帮助", "希望这能帮你", "希望帮到你", "希望能帮助你",
	"如有疑问", "如果有疑问", "如有问题", "如果有问题", "如果还有问题", "如果还有疑问",
	"欢迎随时提问", "欢迎随时", "欢迎交流", "欢迎讨论",
	"随时提问", "随时告诉我", "随时联系我", "有问题随时",
	"感谢阅读", "谢谢阅读", "感谢你的阅读", "谢谢", "感谢",
	"祝你", "祝好", "祝顺利",
	// English closing filler.
	"hope this helps", "hope that helps", "hope it helps", "hope this is helpful",
	"let me know", "feel free", "happy to help",
	"thanks for reading", "thank you for reading", "thanks", "thank you",
	"good luck", "happy coding", "cheers",
}

// fillerFindings reports filler openers and closers. A whole paragraph that
// is essentially only politeness is material; everything else is minor.
func fillerFindings(doc *document) []Finding {
	prose := proseBlocks(doc)
	if len(prose) == 0 {
		return nil
	}
	findings := make([]Finding, 0, 2)
	used := make(map[int]bool, fillerParagraphWindow*2)

	openingWindow := fillerParagraphWindow
	if openingWindow > len(prose) {
		openingWindow = len(prose)
	}
	for i := 0; i < openingWindow; i++ {
		b := prose[i]
		plain := blockPlainText(b)
		norm := normalize(plain)
		if runeCount(norm) < fillerMinParagraphRunes {
			continue
		}
		phrase := matchOpening(norm)
		if phrase == "" {
			continue
		}
		severity := SeverityMinor
		if isPureFillerParagraph(plain, norm) {
			severity = SeverityMaterial
		}
		used[b.paraID] = true
		findings = append(findings, Finding{
			Type:     FindingFillerOpening,
			Severity: severity,
			Location: paragraphLocation(b.paraID),
			Evidence: evidenceFrom(plain),
			Detail:   fmt.Sprintf("opening paragraph starts with filler phrase %q", phrase),
		})
		if len(findings) >= maxFindingsPerType {
			return findings
		}
	}

	closingWindow := fillerParagraphWindow
	if closingWindow > len(prose) {
		closingWindow = len(prose)
	}
	for k := 0; k < closingWindow; k++ {
		b := prose[len(prose)-1-k]
		if used[b.paraID] {
			continue
		}
		plain := blockPlainText(b)
		norm := normalize(plain)
		if runeCount(norm) < fillerMinParagraphRunes {
			continue
		}
		phrase := matchClosing(norm)
		if phrase == "" {
			continue
		}
		severity := SeverityMinor
		if isPureFillerParagraph(plain, norm) {
			severity = SeverityMaterial
		}
		findings = append(findings, Finding{
			Type:     FindingFillerClosing,
			Severity: severity,
			Location: paragraphLocation(b.paraID),
			Evidence: evidenceFrom(plain),
			Detail:   fmt.Sprintf("closing paragraph ends with filler phrase %q", phrase),
		})
		if len(findings) >= maxFindingsPerType {
			return findings
		}
	}
	return findings
}

// proseBlocks returns prose blocks only; headings, code, tables, quotes and
// lists are never filler candidates.
func proseBlocks(doc *document) []*block {
	blocks := make([]*block, 0, len(doc.blocks))
	for i := range doc.blocks {
		if doc.blocks[i].kind == blockProse {
			blocks = append(blocks, &doc.blocks[i])
		}
	}
	return blocks
}

// matchOpening returns the longest filler phrase that starts norm.
func matchOpening(norm string) string {
	hay := []rune(norm)
	best := ""
	bestLen := 0
	for _, p := range fillerOpeningPhrases {
		phrase := []rune(normalize(p))
		if len(phrase) == 0 || len(phrase) > len(hay) {
			continue
		}
		if !hasRunePrefix(hay, phrase) {
			continue
		}
		if !asciiPhraseBoundary(phrase, hay[len(phrase):]) {
			continue
		}
		if len(phrase) > bestLen {
			best, bestLen = string(phrase), len(phrase)
		}
	}
	return best
}

// matchClosing returns the longest filler phrase that appears near the end of
// norm. Short closing paragraphs are matched anywhere inside them.
func matchClosing(norm string) string {
	hay := []rune(norm)
	total := len(hay)
	best := ""
	bestLen := 0
	for _, p := range fillerClosingPhrases {
		phrase := []rune(normalize(p))
		if len(phrase) == 0 || len(phrase) > total {
			continue
		}
		pos := lastIndexRunes(hay, phrase)
		if pos < 0 {
			continue
		}
		end := pos + len(phrase)
		if !asciiPhraseBoundary(phrase, hay[end:]) {
			continue
		}
		nearEnd := total <= fillerShortClosingRunes || end == total || end*3 >= total*2
		if !nearEnd {
			continue
		}
		if len(phrase) > bestLen {
			best, bestLen = string(phrase), len(phrase)
		}
	}
	return best
}

// asciiPhraseBoundary rejects matches such as "sure" inside "surely": an ASCII
// alphanumeric phrase must not be followed by another ASCII alphanumeric rune.
func asciiPhraseBoundary(phrase, rest []rune) bool {
	if len(phrase) == 0 {
		return true
	}
	last := phrase[len(phrase)-1]
	if !isASCIIAlnum(last) || len(rest) == 0 {
		return true
	}
	return !isASCIIAlnum(rest[0])
}

func isASCIIAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func hasRunePrefix(hay, prefix []rune) bool {
	if len(prefix) > len(hay) {
		return false
	}
	for i := range prefix {
		if hay[i] != prefix[i] {
			return false
		}
	}
	return true
}

func indexRunes(hay, needle []rune, from int) int {
	if from < 0 {
		from = 0
	}
	for i := from; i+len(needle) <= len(hay); i++ {
		match := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func lastIndexRunes(hay, needle []rune) int {
	last := -1
	for from := 0; ; {
		pos := indexRunes(hay, needle, from)
		if pos < 0 {
			return last
		}
		last = pos
		from = pos + 1
	}
}

// isPureFillerParagraph reports whether a paragraph is essentially only filler:
// filler phrases dominate it and it carries no facts, numbers, code or links.
func isPureFillerParagraph(plain, norm string) bool {
	runes := []rune(norm)
	if len(runes) < fillerPureMinRunes || len(runes) > fillerPureMaxRunes {
		return false
	}
	if containsDigit(plain) || containsBacktick(plain) || containsURL(plain) {
		return false
	}
	return fillerCoverage(runes) >= fillerPureCoverage
}

// fillerCoverage is the share of norm covered by non-overlapping filler
// phrases from both curated lists.
func fillerCoverage(norm []rune) float64 {
	if len(norm) == 0 {
		return 0
	}
	covered := make([]bool, len(norm))
	for _, list := range [][]string{fillerOpeningPhrases, fillerClosingPhrases} {
		for _, p := range list {
			phrase := []rune(normalize(p))
			if len(phrase) == 0 {
				continue
			}
			for from := 0; ; {
				pos := indexRunes(norm, phrase, from)
				if pos < 0 {
					break
				}
				for k := pos; k < pos+len(phrase) && k < len(covered); k++ {
					covered[k] = true
				}
				from = pos + len(phrase)
			}
		}
	}
	n := 0
	for _, c := range covered {
		if c {
			n++
		}
	}
	return float64(n) / float64(len(norm))
}
