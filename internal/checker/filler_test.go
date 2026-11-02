package checker

import (
	"strings"
	"testing"
)

func TestFillerOpeningAndClosingChinese(t *testing.T) {
	text := "好的，我来帮你分析一下这个问题。\n\n" +
		"panic 说明解引用了 nil 指针，先看栈里第一行业务代码，再检查 defer recover 是否只兜住了当前 goroutine。\n\n" +
		"希望以上内容对你有帮助。"
	rep := Analyze(text)

	openings := findingsOfType(rep, FindingFillerOpening)
	if len(openings) != 1 {
		t.Fatalf("filler_opening findings = %#v, want 1", openings)
	}
	if openings[0].Severity != SeverityMinor {
		t.Errorf("opening severity = %q, want minor", openings[0].Severity)
	}
	if openings[0].Location != "paragraph_1" {
		t.Errorf("opening location = %q, want paragraph_1", openings[0].Location)
	}

	closings := findingsOfType(rep, FindingFillerClosing)
	if len(closings) != 1 {
		t.Fatalf("filler_closing findings = %#v, want 1", closings)
	}
	if closings[0].Severity != SeverityMaterial {
		t.Errorf("pure filler closer severity = %q, want material", closings[0].Severity)
	}
}

func TestFillerPureOpeningIsMaterial(t *testing.T) {
	text := "当然可以！希望这些内容对你有帮助！\n\n" +
		"真正的结论是：先检查超时配置，再看重试次数是否合理。"
	rep := Analyze(text)
	openings := findingsOfType(rep, FindingFillerOpening)
	if len(openings) != 1 {
		t.Fatalf("filler_opening findings = %#v, want 1", openings)
	}
	if openings[0].Severity != SeverityMaterial {
		t.Errorf("severity = %q, want material for a pure filler paragraph", openings[0].Severity)
	}
}

func TestFillerOpeningEnglish(t *testing.T) {
	text := "Great question! I'd be happy to help you with this.\n\n" +
		"The 403 comes from a signature mismatch, so compare request time with the server clock.\n\n" +
		"Hope this helps! Let me know if you have any questions."
	rep := Analyze(text)

	openings := findingsOfType(rep, FindingFillerOpening)
	if len(openings) != 1 {
		t.Fatalf("filler_opening findings = %#v, want 1", openings)
	}
	closings := findingsOfType(rep, FindingFillerClosing)
	if len(closings) != 1 {
		t.Fatalf("filler_closing findings = %#v, want 1", closings)
	}
	if closings[0].Severity != SeverityMinor {
		t.Errorf("english closer severity = %q, want minor (not pure filler)", closings[0].Severity)
	}
}

func TestFillerClosingSwappedOrder(t *testing.T) {
	text := "先写库再删缓存，顺序反了就会出现旧值回填。\n\n" +
		"如果还有疑问欢迎随时提问，希望以上内容对你有帮助。"
	rep := Analyze(text)
	closings := findingsOfType(rep, FindingFillerClosing)
	if len(closings) != 1 {
		t.Fatalf("filler_closing findings = %#v, want 1 even with swapped closer order", closings)
	}
	if closings[0].Severity != SeverityMaterial {
		t.Errorf("severity = %q, want material for a pure filler closer", closings[0].Severity)
	}
}

func TestFillerIgnoresHeadingAndCode(t *testing.T) {
	fence := "```"
	text := "## 排查步骤\n\n" +
		fence + "text\n好的，我来帮你分析一下这个问题。\n" + fence + "\n\n" +
		"好的，下面逐个排查。\n\n" +
		"先看退出码，再看日志，最后看探针配置，这三步能覆盖大多数启动失败。"
	rep := Analyze(text)
	openings := findingsOfType(rep, FindingFillerOpening)
	if len(openings) != 1 {
		t.Fatalf("filler_opening findings = %#v, want 1 (heading and code ignored)", openings)
	}
	if openings[0].Location != "paragraph_1" {
		t.Errorf("location = %q, want paragraph_1 (first prose paragraph)", openings[0].Location)
	}
}

func TestFillerSubstantiveAnswerHasNoFiller(t *testing.T) {
	text := "Go 的 context 包用于在调用链之间传递取消信号和超时。\n\n" +
		"在 main 里创建带超时的 context，再把它传进下游函数，最后记得调用 cancel 释放资源。"
	rep := Analyze(text)
	if findings := findingsOfType(rep, FindingFillerOpening); len(findings) != 0 {
		t.Errorf("unexpected filler_opening findings: %#v", findings)
	}
	if findings := findingsOfType(rep, FindingFillerClosing); len(findings) != 0 {
		t.Errorf("unexpected filler_closing findings: %#v", findings)
	}
}

func TestFillerNeverFullAnswerInEvidence(t *testing.T) {
	longOpening := "好的，" + strings.Repeat("我来帮你分析一下这个问题，", 20)
	rep := Analyze(longOpening + "\n\n接下来的正文段落说明具体排查方法。")
	for _, f := range rep.Findings {
		if runeCount(f.Evidence) > maxEvidenceRunes {
			t.Errorf("finding %#v has oversized evidence (%d runes)", f, runeCount(f.Evidence))
		}
	}
}
