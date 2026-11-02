package checker

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"fullwidth punctuation", "你好，世界！", "你好,世界!"},
		{"fullwidth latin and digits", "ＡＢＣ　１２３", "abc 123"},
		{"ascii lowercasing", "Hello WORLD", "hello world"},
		{"emphasis and inline code", "**Bold** and *em* and ~~strike~~ and `code`", "bold and em and strike and code"},
		{"link keeps text", "见 [官方文档](https://example.com/doc)。", "见官方文档."},
		{"image keeps alt text", "![架构图](a.png) 展示了调用链。", "架构图展示了调用链."},
		{"whitespace collapse", "a   b\n\tc", "a b c"},
		{"cjk spaces removed", "重复 段落", "重复段落"},
		{"quote and list markers", "> - item one", "item one"},
		{"heading marker", "## 标题", "标题"},
		{"list marker", "- 第一项", "第一项"},
		{"ordered list marker", "1. 第一项", "第一项"},
		{"fullwidth colon", "注意：这里", "注意:这里"},
		{"ellipsis", "等等…结束", "等等...结束"},
		{"cjk quotes", "他说：“可以”。", "他说:\"可以\"."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalize(tt.in); got != tt.want {
				t.Errorf("normalize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeComparable(t *testing.T) {
	// The same sentence written with different Markdown must normalize to
	// exactly the same comparison form.
	variants := []string{
		"**缓存**很重要，可以降低数据库压力。",
		"缓存很重要, 可以降低数据库压力.",
		"缓存很重要，可以降低数据库压力。",
	}
	want := normalize(variants[0])
	for _, v := range variants[1:] {
		if got := normalize(v); got != want {
			t.Errorf("normalize(%q) = %q, want %q", v, got, want)
		}
	}
}

func TestSplitSentences(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"chinese", "第一句。第二句！第三句？", []string{"第一句。", "第二句！", "第三句？"}},
		{"chinese semicolon", "一是快；二是稳。", []string{"一是快；", "二是稳。"}},
		{"english", "First sentence. Second one! Third?", []string{"First sentence.", "Second one!", "Third?"}},
		{"decimal and version", "版本 1.2.3 已发布。", []string{"版本 1.2.3 已发布。"}},
		{"abbreviation", "Use e.g. pprof here. Done.", []string{"Use e.g. pprof here.", "Done."}},
		{"filename dot", "打开 go.mod 文件。", []string{"打开 go.mod 文件。"}},
		{"newline boundary", "line one\nline two", []string{"line one", "line two"}},
		{"closing quote", "他说：“可以。”然后离开了。", []string{"他说：“可以。”", "然后离开了。"}},
		{"empty", "", nil},
		{"no terminator", "只有一句话没有标点", []string{"只有一句话没有标点"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitSentences(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("splitSentences(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("sentence %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestEvidenceTruncation(t *testing.T) {
	long := strings.Repeat("这是一段很长的段落内容。", 80)
	got := evidenceFrom(long)
	if n := utf8.RuneCountInString(got); n > maxEvidenceRunes {
		t.Fatalf("evidence has %d runes, want <= %d", n, maxEvidenceRunes)
	}
	if got == "" {
		t.Fatal("evidence must not be empty for non-empty input")
	}
}

func TestParseDocument(t *testing.T) {
	fence := "```"
	text := "第一段。第二段。\n\n" +
		fence + "go\nfunc main() {}\n" + fence + "\n\n" +
		"| a | b |\n| --- | --- |\n| 1 | 2 |\n\n" +
		"- 项目一\n- 项目二\n"
	doc := parseDocument(text)

	if doc.stats.CodeBlocks != 1 {
		t.Errorf("CodeBlocks = %d, want 1", doc.stats.CodeBlocks)
	}
	if doc.stats.Paragraphs != 2 {
		t.Errorf("Paragraphs = %d, want 2 (one prose, one list)", doc.stats.Paragraphs)
	}
	if doc.stats.Sentences != 4 {
		t.Errorf("Sentences = %d, want 4", doc.stats.Sentences)
	}
	wantNatural := "第一段。第二段。\n- 项目一\n- 项目二"
	if doc.stats.NaturalRunes != utf8.RuneCountInString(wantNatural) {
		t.Errorf("NaturalRunes = %d, want %d", doc.stats.NaturalRunes, utf8.RuneCountInString(wantNatural))
	}
	if doc.stats.NaturalRunes >= doc.stats.Runes {
		t.Errorf("NaturalRunes = %d must exclude code and table markup from Runes = %d",
			doc.stats.NaturalRunes, doc.stats.Runes)
	}

	kinds := make([]blockKind, len(doc.blocks))
	for i := range doc.blocks {
		kinds[i] = doc.blocks[i].kind
	}
	wantKinds := []blockKind{blockProse, blockTable, blockList}
	if len(kinds) != len(wantKinds) {
		t.Fatalf("blocks = %v, want %v", kinds, wantKinds)
	}
	for i := range wantKinds {
		if kinds[i] != wantKinds[i] {
			t.Errorf("block %d kind = %v, want %v", i, kinds[i], wantKinds[i])
		}
	}
}
