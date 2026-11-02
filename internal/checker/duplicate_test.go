package checker

import (
	"strings"
	"testing"
)

func TestDuplicateParagraphExact(t *testing.T) {
	text := "**缓存**很重要，[Redis](https://redis.io) 可以显著降低数据库压力，提升响应速度，适合读多写少的场景。\n\n" +
		"中间插入一段完全不同的说明，讲的是别的主题，比如连接池大小和超时配置。\n\n" +
		"缓存很重要，Redis 可以显著降低数据库压力，提升响应速度，适合读多写少的场景。"
	rep := Analyze(text)

	dups := findingsOfType(rep, FindingDuplicateParagraph)
	if len(dups) != 1 {
		t.Fatalf("duplicate_paragraph findings = %#v, want exactly 1", dups)
	}
	got := dups[0]
	if got.Severity != SeverityMaterial {
		t.Errorf("severity = %q, want %q", got.Severity, SeverityMaterial)
	}
	if got.Location != "paragraph_3" {
		t.Errorf("location = %q, want paragraph_3", got.Location)
	}
	if got.Evidence == "" || runeCount(got.Evidence) > maxEvidenceRunes {
		t.Errorf("evidence = %q, want 1..%d runes", got.Evidence, maxEvidenceRunes)
	}
	if !rep.HasMaterial() {
		t.Errorf("duplicate paragraph must be material: %#v", rep.Findings)
	}
}

func TestDuplicateParagraphNearSimilar(t *testing.T) {
	p1 := "缓存层的主要作用是降低数据库压力并提升响应速度，通常适合读多写少且可以容忍短暂数据不一致的业务场景，" +
		"例如商品详情、配置中心和会话数据这类高频读取的接口。"
	p2 := "缓存层的主要作用是降低数据库压力并提升响应效率，通常适合读多写少且可以容忍短暂数据不一致的业务场景，" +
		"例如商品详情、配置中心和会话数据这类高频读取的接口。"
	rep := Analyze(p1 + "\n\n" + p2)

	dups := findingsOfType(rep, FindingDuplicateParagraph)
	if len(dups) == 0 {
		t.Fatalf("expected a near-duplicate paragraph finding, got %#v (stats: %+v)", rep.Findings, rep.Stats)
	}
	found := false
	for _, f := range dups {
		if f.Severity == SeverityMaterial && strings.Contains(f.Detail, "near-duplicate") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a material near-duplicate detail, got %#v", dups)
	}
}

func TestDuplicateParagraphIgnoresCode(t *testing.T) {
	line := "fmt.Println(\"缓存很重要，这行代码在代码块里重复出现。\")"
	fence := "```"
	text := fence + "go\n" + line + "\n" + fence + "\n\n" + fence + "go\n" + line + "\n" + fence
	rep := Analyze(text)
	if len(rep.Findings) != 0 {
		t.Fatalf("code blocks must be excluded from duplicate analysis, got %#v", rep.Findings)
	}
	if rep.Stats.CodeBlocks != 2 {
		t.Errorf("CodeBlocks = %d, want 2", rep.Stats.CodeBlocks)
	}
}

func TestDuplicateParagraphInQuoteReported(t *testing.T) {
	para := "缓存会显著降低数据库压力，但必须结合失效策略一起设计。"
	quote := "> " + para
	rep := Analyze(para + "\n\n" + quote)
	if dups := findingsOfType(rep, FindingDuplicateParagraph); len(dups) == 0 {
		t.Fatalf("a duplicated quoted paragraph must still be reported, got %#v", rep.Findings)
	}
}

func TestDuplicateParagraphIgnoresTables(t *testing.T) {
	table := "| 指标 | 说明 |\n| --- | --- |\n| 延迟 | 响应时间 |\n| 吞吐 | 每秒请求数 |"
	rep := Analyze(table + "\n\n" + table)
	if len(rep.Findings) != 0 {
		t.Fatalf("tables must not be flagged as duplicate paragraphs, got %#v", rep.Findings)
	}
}

func TestDuplicateParagraphListsFormattingOnly(t *testing.T) {
	list1 := "- 安装项目依赖包\n- 运行完整测试套件\n- 提交代码到远程仓库"
	list2 := "- 配置开发环境变量\n- 启动本地调试服务\n- 查看服务运行日志"
	rep := Analyze(list1 + "\n\n" + list2)
	if dups := findingsOfType(rep, FindingDuplicateParagraph); len(dups) != 0 {
		t.Fatalf("shared list markers alone must not be a duplicate, got %#v", dups)
	}
}

func TestDuplicateParagraphShortIgnored(t *testing.T) {
	rep := Analyze("好的。\n\n好的。")
	if len(rep.Findings) != 0 {
		t.Fatalf("short paragraph repetition must be ignored, got %#v", rep.Findings)
	}
}

func TestDuplicateSentenceExact(t *testing.T) {
	sentence := "缓存可以显著提升系统的响应速度并降低数据库压力。"
	text := "先说明背景，数据库压力主要来自重复查询。\n\n" +
		sentence + "所以缓存配置很关键。\n\n" +
		"中间段落讲点别的，比如连接池大小和超时参数如何设置。\n\n" +
		sentence + "这就是最终结论。"
	rep := Analyze(text)

	dups := findingsOfType(rep, FindingDuplicateSentence)
	if len(dups) != 1 {
		t.Fatalf("duplicate_sentence findings = %#v, want exactly 1", dups)
	}
	got := dups[0]
	if got.Severity != SeverityMaterial {
		t.Errorf("severity = %q, want %q", got.Severity, SeverityMaterial)
	}
	if !strings.Contains(got.Detail, "exact duplicate") {
		t.Errorf("detail = %q, want exact duplicate note", got.Detail)
	}
	if got.Location == "" || got.Evidence == "" {
		t.Errorf("finding must carry location and evidence: %#v", got)
	}
}

func TestDuplicateSentenceShortIgnored(t *testing.T) {
	rep := Analyze("完成。\n\n完成。")
	if len(rep.Findings) != 0 {
		t.Fatalf("short sentence repetition must be ignored, got %#v", rep.Findings)
	}
}

func TestDuplicateSentenceEnglish(t *testing.T) {
	sentence := "The singleflight package collapses concurrent requests for the same cache key into one upstream call."
	text := "First, a paragraph explains the cache layout in enough detail for background context.\n\n" +
		"Then the key point is straightforward. " + sentence + " That collapse keeps the backend load low.\n\n" +
		"Finally the conclusion is clear. " + sentence
	rep := Analyze(text)

	dups := findingsOfType(rep, FindingDuplicateSentence)
	if len(dups) == 0 {
		t.Fatalf("expected duplicate_sentence for repeated English sentence, got %#v", rep.Findings)
	}
	if dups[0].Severity != SeverityMaterial {
		t.Errorf("severity = %q, want material for a long exact duplicate", dups[0].Severity)
	}
}

func TestDuplicateSentenceNearSimilar(t *testing.T) {
	s1 := "When the cache key is missing the client should collapse concurrent requests into a single upstream call and populate the cache afterwards."
	s2 := "When the cache key is missing the client should collapse concurrent requests into a single upstream path and populate the cache afterwards."
	rep := Analyze(s1 + "\n\n" + s2)

	dups := findingsOfType(rep, FindingDuplicateSentence)
	if len(dups) == 0 {
		t.Fatalf("expected a near-duplicate sentence finding, got %#v", rep.Findings)
	}
	if !strings.Contains(dups[0].Detail, "near-duplicate") {
		t.Errorf("detail = %q, want near-duplicate note", dups[0].Detail)
	}
}

func TestDuplicateSentenceIgnoresCode(t *testing.T) {
	fence := "```"
	line := "cache = load_cache(key); cache = load_cache(key);"
	text := fence + "python\n" + line + "\n" + line + "\n" + fence
	rep := Analyze(text)
	if len(rep.Findings) != 0 {
		t.Fatalf("code blocks must be excluded from duplicate analysis, got %#v", rep.Findings)
	}
}
