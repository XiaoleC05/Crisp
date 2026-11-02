package checker

import (
	"fmt"
	"strings"
	"testing"
)

func TestStructureTinyListItems(t *testing.T) {
	text := "- 快\n- 简单\n- 省事\n- 稳定\n- 直观\n- 通用\n- 灵活\n- 可扩展\n- 易维护"
	rep := Analyze(text)
	findings := findingsOfType(rep, FindingStructureAnomaly)
	if len(findings) != 1 {
		t.Fatalf("structure_anomaly findings = %#v, want 1", findings)
	}
	if findings[0].Severity != SeverityMinor {
		t.Errorf("severity = %q, want minor", findings[0].Severity)
	}
	if rep.HasMaterial() {
		t.Errorf("structure findings must never be material: %#v", rep.Findings)
	}
}

func TestStructureFragmentedParagraphs(t *testing.T) {
	sentences := []string{
		"先打开命令行终端。",
		"确认 Python 已经安装。",
		"检查 pip 是否可用。",
		"升级 pip 到最新版本。",
		"安装 requests 依赖包。",
		"验证导入是否成功。",
		"最后清理临时文件。",
	}
	rep := Analyze(strings.Join(sentences, "\n\n"))
	findings := findingsOfType(rep, FindingStructureAnomaly)
	if len(findings) != 1 {
		t.Fatalf("structure_anomaly findings = %#v, want 1 for one-sentence paragraphs", findings)
	}
	if findings[0].Severity != SeverityMinor {
		t.Errorf("severity = %q, want minor", findings[0].Severity)
	}
}

func TestStructureDoesNotFireOnNormalAnswer(t *testing.T) {
	text := "先看退出码，它能直接区分三类问题，命令输出里会给出原因和退出码。\n\n" +
		"第二步看日志，上一个实例的日志通常能说明配置问题，启动瞬间退出的容器尤其如此。\n\n" +
		"第三步看事件和探针，探针初始延迟太短会导致无限重启，启动慢的服务应该改用 startupProbe。\n\n" +
		"最后排查资源限制，内存上限接近峰值时一旦有流量波动就会 OOMKilled。"
	rep := Analyze(text)
	if findings := findingsOfType(rep, FindingStructureAnomaly); len(findings) != 0 {
		t.Fatalf("structure rule must not fire on a normal multi-sentence answer: %#v", findings)
	}
}

func TestStructureLengthSignalInfoOnly(t *testing.T) {
	places := []string{"缓存层", "网络模块", "存储引擎", "调度器", "校验器", "序列化层"}
	actions := []string{"降级", "重试", "限流", "熔断", "预热", "回填"}
	targets := []string{"连接池", "内存配额", "磁盘队列", "请求队列", "线程池", "日志缓冲"}
	var b strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&b, "第%d条排查说明指出，%s在%s时需要重点观察%s的负载变化，并结合监控指标确认是否触发了保护策略。\n\n",
			i, places[i%6], actions[(i/2)%6], targets[(i/3)%6])
	}
	rep := Analyze(b.String())

	if rep.Stats.NaturalRunes < longAnswerNaturalRunes {
		t.Fatalf("NaturalRunes = %d, want >= %d for this fixture", rep.Stats.NaturalRunes, longAnswerNaturalRunes)
	}
	length := findingsOfType(rep, FindingLengthSignal)
	if len(length) != 1 {
		t.Fatalf("length_signal findings = %#v, want 1", length)
	}
	if length[0].Severity != SeverityInfo {
		t.Errorf("length severity = %q, want info (never material)", length[0].Severity)
	}
}

func TestStructureLengthSignalAbsentForShortAnswer(t *testing.T) {
	rep := Analyze("用 runtime.NumCPU() 读取逻辑 CPU 核数，用 runtime.GOMAXPROCS(0) 读取当前 GOMAXPROCS 值。")
	if findings := findingsOfType(rep, FindingLengthSignal); len(findings) != 0 {
		t.Fatalf("short answer must not emit a length signal: %#v", findings)
	}
	if rep.HasMaterial() {
		t.Fatalf("short answer must not emit material findings: %#v", rep.Findings)
	}
}
func TestStructureRepetitiveBulletScaffold(t *testing.T) {
	items := []string{
		"- 检查连接池的最大连接数是否足够，避免高峰期出现连接耗尽并导致核心接口整体不可用。",
		"- 检查连接池的空闲连接回收策略，避免连接长时间闲置并占用数据库侧的资源配额。",
		"- 检查连接池的超时配置是否合理，避免请求无限等待并把上游线程池一起拖垮。",
		"- 检查连接池的泄漏检测开关，避免连接被业务代码遗忘后无法自动回收。",
		"- 检查连接池的健康检查间隔，避免使用已经被数据库服务端主动关闭的失效连接。",
		"- 检查连接池的初始化数量，避免服务启动后集中建立连接造成数据库瞬时压力。",
		"- 检查连接池的监控指标上报，避免连接耗尽问题在生产环境中无法被及时发现。",
		"- 检查连接池的容量评估方式，避免只凭经验估算而没有压测数据支撑。",
	}
	rep := Analyze(strings.Join(items, "\n"))
	findings := findingsOfType(rep, FindingStructureAnomaly)
	if len(findings) != 1 {
		t.Fatalf("structure_anomaly findings = %#v, want 1 for repetitive scaffold", findings)
	}
	if findings[0].Severity != SeverityInfo {
		t.Errorf("scaffold severity = %q, want info", findings[0].Severity)
	}
}
