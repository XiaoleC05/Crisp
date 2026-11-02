package checker

import (
	"reflect"
	"strings"
	"testing"
)

// findingsOfType returns every finding of one type, preserving rule order.
func findingsOfType(rep Report, typ FindingType) []Finding {
	var out []Finding
	for _, f := range rep.Findings {
		if f.Type == typ {
			out = append(out, f)
		}
	}
	return out
}

func TestAnalyzeEmptyInputs(t *testing.T) {
	for _, in := range []string{"", "   ", "\n\n\n", "\t\n \n"} {
		rep := Analyze(in)
		if len(rep.Findings) != 0 {
			t.Errorf("Analyze(%q) findings = %#v, want none", in, rep.Findings)
		}
		if rep.Stats.Runes != runeCount(in) {
			t.Errorf("Analyze(%q) Runes = %d, want %d", in, rep.Stats.Runes, runeCount(in))
		}
		if rep.Stats.NaturalRunes != 0 {
			t.Errorf("Analyze(%q) NaturalRunes = %d, want 0", in, rep.Stats.NaturalRunes)
		}
	}
}

func TestAnalyzeStats(t *testing.T) {
	fence := "```"
	text := "第一段。第二段。\n\n" +
		fence + "go\nfmt.Println(1)\n" + fence + "\n\n" +
		"| a | b |\n| --- | --- |\n| 1 | 2 |\n\n" +
		"- 项目一\n- 项目二\n"
	rep := Analyze(text)

	if got, want := rep.Stats.Runes, runeCount(text); got != want {
		t.Errorf("Runes = %d, want %d (whole input)", got, want)
	}
	if rep.Stats.CodeBlocks != 1 {
		t.Errorf("CodeBlocks = %d, want 1", rep.Stats.CodeBlocks)
	}
	if rep.Stats.Paragraphs != 2 {
		t.Errorf("Paragraphs = %d, want 2", rep.Stats.Paragraphs)
	}
	if rep.Stats.Sentences != 4 {
		t.Errorf("Sentences = %d, want 4", rep.Stats.Sentences)
	}
	wantNatural := "第一段。第二段。\n- 项目一\n- 项目二"
	if got, want := rep.Stats.NaturalRunes, runeCount(wantNatural); got != want {
		t.Errorf("NaturalRunes = %d, want %d (code and table markup excluded)", got, want)
	}
	if len(rep.Findings) != 0 {
		t.Errorf("findings = %#v, want none", rep.Findings)
	}
}

func TestAnalyzeDeterministic(t *testing.T) {
	text := "好的，我来帮你分析一下这个问题。\n\n" +
		"缓存可以显著提升系统的响应速度并降低数据库压力。\n\n" +
		"中间段落说明连接池大小和超时参数如何配置，这里不重复前面的结论。\n\n" +
		"缓存可以显著提升系统的响应速度并降低数据库压力。\n\n" +
		"希望以上内容对你有帮助。"
	first := Analyze(text)
	second := Analyze(text)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Analyze is not deterministic:\nfirst  = %#v\nsecond = %#v", first, second)
	}
}

func TestAnalyzeNeverPanics(t *testing.T) {
	inputs := []string{
		string([]byte{0xff, 0xfe, 0x00, 0x01}),
		"\u200b\u200c\ufeff",
		strings.Repeat("```", 500),
		strings.Repeat("[[[[[", 2000),
		strings.Repeat("a", 1<<14),
		strings.Repeat("这是一段正常的中文内容。", 2000),
		strings.Repeat("> ", 1000),
		strings.Repeat("# ", 1000),
		"| a | b |\n" + strings.Repeat("| --- | --- |\n", 50),
		strings.Repeat("- ", 1000),
	}
	for i, in := range inputs {
		rep := Analyze(in)
		if rep.Stats.Runes != runeCount(in) {
			t.Errorf("input %d: Runes = %d, want %d", i, rep.Stats.Runes, runeCount(in))
		}
		if rep.Stats.NaturalRunes < 0 || rep.Stats.NaturalRunes > rep.Stats.Runes {
			t.Errorf("input %d: NaturalRunes = %d out of range [0,%d]", i, rep.Stats.NaturalRunes, rep.Stats.Runes)
		}
	}
}

func TestAnalyzeEvidenceBounded(t *testing.T) {
	para := strings.Repeat("这是一段用于检测重复段落的长文本内容。", 12)
	rep := Analyze(para + "\n\n" + para + "\n\n" + para)
	if len(rep.Findings) == 0 {
		t.Fatal("expected findings for three identical long paragraphs")
	}
	if !rep.HasMaterial() {
		t.Errorf("identical long paragraphs should produce a material finding: %#v", rep.Findings)
	}
	for _, f := range rep.Findings {
		if n := runeCount(f.Evidence); n > maxEvidenceRunes {
			t.Errorf("finding %#v has %d evidence runes, want <= %d", f, n, maxEvidenceRunes)
		}
	}
}

func TestReportHelpers(t *testing.T) {
	rep := Report{Findings: []Finding{
		{Type: FindingDuplicateParagraph, Severity: SeverityMaterial},
		{Type: FindingFillerOpening, Severity: SeverityMinor},
		{Type: FindingDuplicateParagraph, Severity: SeverityInfo},
	}}
	if !rep.HasMaterial() {
		t.Error("HasMaterial() = false, want true")
	}
	want := []FindingType{FindingDuplicateParagraph, FindingFillerOpening}
	if got := rep.Types(); !reflect.DeepEqual(got, want) {
		t.Errorf("Types() = %v, want %v", got, want)
	}

	empty := Report{}
	if empty.HasMaterial() {
		t.Error("empty Report.HasMaterial() = true, want false")
	}
	if got := empty.Types(); len(got) != 0 {
		t.Errorf("empty Report.Types() = %v, want empty", got)
	}
}
