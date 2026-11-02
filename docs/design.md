# Crisp 设计与实施规格（v0.1 MVP）

## 1. 项目定位

Crisp 是一个独立的 Go 输出质量检查器。它在 Agent 完成回答、交付用户之前检查回答质量，
只拦截存在明确实质问题的回答，其他情况一律放行。

第一版接入方式：Codex CLI Stop Hook。质量检查核心与 Agent 框架解耦，后续扩展接入层。

- 二进制：`crisp`
- Go 模块：`github.com/XiaoleC05/Crisp`
- 本机验证用 Codex CLI：`0.145.0-alpha.18`（路径见 test/e2e 报告，需 E2E 验证 hooks 支持）

## 2. 要解决的问题

1. 重复表达：同一个观点换不同说法反复出现。
2. 无效铺垫：客套话、背景复述、没有信息量的开场白。
3. 过度拆分：一句话能说明的问题拆成多个小点。
4. 无效解释：反复解释用户已经知道的内容。
5. 结论重复：开头、正文、结尾重复同一结论。
6. 信息密度低：文字很多，新增有效信息很少。

## 3. 核心原则（硬约束）

- 简洁不等于字数少。
- 不允许为了精简丢失事实、数字、代码和必要解释。
- 正常回答默认放行。
- 只有明确存在实质性质量问题才触发重写。
- 每个 Turn 最多触发一次重写。
- 任何内部异常（解析失败、超时、网络错误、Critic 非法 JSON、规则 panic）→ FAIL-OPEN，输出 `{}`。
- stdout 永远只输出一个合法 JSON 对象；所有日志写 stderr。
- 不通过正则删除内容，只要求 Agent 重新生成完整回答。

## 4. 架构

```text
Codex CLI (Stop hook)
   | stdin: hook JSON
   v
crisp hook （cmd/crisp/main.go）
   |  1. hook.Parse + 防循环守卫（stop_hook_active / 空消息）
   |  2. checker.Analyze           确定性规则
   |  3. policy.NeedsCritic        是否调用 Critic（balanced / economy）
   |  4. critic.Client.Review      OpenAI-compatible 语义审查
   |  5. policy.Decide             PASS / BLOCK / FAIL-OPEN
   v stdout: {} 或 {"decision":"block","reason":"..."}
```

## 5. 模块与文件职责

| 路径 | 职责 | 负责人 |
| --- | --- | --- |
| `cmd/crisp/main.go` | 进程入口、Hook 流程编排、env 配置、超时、fail-open、check-text 测试入口 | Lead |
| `internal/hook/codex.go` | Hook 输入解析、输出 JSON、transcript 最近用户消息提取（限读） | Lead |
| `internal/checker/` | 确定性检查：重复段落/句子、无效开场白/结束语、结构异常、字数信号 | checker-dev |
| `internal/critic/` | OpenAI-compatible 客户端、prompt、响应 schema 校验 | critic-dev |
| `internal/policy/decision.go` | 是否需要 Critic、最终决策、重写指令 | Lead |
| `internal/logger/logger.go` | stderr 结构化 JSON 日志、敏感字段过滤 | Lead |
| `testdata/cases/` | 30 条回归样本（positive 20 / negative 10） | samples-dev |
| `test/e2e/`、`scripts/`、`.codex/` | 真实 Codex CLI E2E、超时验证、hook 安装脚本 | codex-e2e |

约束：只使用 Go 标准库，不新增第三方依赖；不修改他人负责目录；只有 Lead 执行 git 操作。

## 6. Codex Stop Hook 协议

### 6.1 输入（stdin，单个 JSON 对象）

```go
type Input struct {
    SessionID            string  `json:"session_id"`
    TurnID               string  `json:"turn_id"`
    TranscriptPath       *string `json:"transcript_path"`
    StopHookActive       *bool   `json:"stop_hook_active"`
    LastAssistantMessage *string `json:"last_assistant_message"`
}
```

- 必须使用 `*bool` 区分字段缺失与显式 false。
- 字段缺失时记录协议异常并放行（FAIL-OPEN）。
- 输入上限 4 MiB；空输入、非法 JSON、尾部多余数据都按解析失败处理并放行。

### 6.2 输出（stdout，单个 JSON 对象）

放行：`{}`

重写：

```json
{
  "decision": "block",
  "reason": "请重新输出完整最终回答：合并语义重复的段落，删除无效铺垫和结论重复，保留全部必要事实、数字、代码和解释，不要为了缩短篇幅而删除必要信息。"
}
```

不要输出 `hookSpecificOutput`。

### 6.3 执行顺序（不可调换）

1. 读取 stdin → 解析失败：记录异常，输出 `{}`。
2. `stop_hook_active == true` → 输出 `{}`（本 Turn 已经重写过，最多一次）。
3. `last_assistant_message` 为空 → 输出 `{}`。
4. Deterministic Checker。
5. LLM Critic（按策略）。
6. PASS / 异常 → `{}`；BLOCK → 返回 block JSON。

### 6.4 防循环

Codex 的 `stop_hook_active` 表示此前已因 Stop Hook 触发过续写，平台不强制一次重写限制。
第一版不引入 Redis/数据库：以 `stop_hook_active` 作为主要守卫，配合"每个 Turn 最多一次"的
流程约束。若该字段缺失，记录协议异常并放行。

### 6.5 hooks.json

项目级配置 `.codex/hooks.json`：

```json
{
  "hooks": {
    "Stop": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "D:\\05_Code\\Crisp\\bin\\crisp.exe",
            "timeout": 30,
            "statusMessage": "Checking response quality"
          }
        ]
      }
    ]
  }
}
```

字段名必须是 `timeout`（秒）。`timeout_sec`（Rust 内部字段）与 `timeoutSec`（App Server
序列化字段）都不是 hooks.json 字段；用错可能被静默忽略并回退到 600 秒。

## 7. Deterministic Checker 要求

输入：完成后的回答全文（Markdown）。输出：`Report{Findings, Stats}`，纯函数，永不 panic。

必须实现的检查：

| 检查项 | 方式 | 严重度建议 |
| --- | --- | --- |
| 重复段落 | 规范化后比较段落（去掉 markdown 标记/空白差异），高相似度 | material 交 Critic 确认 |
| 重复句子 | 长句规范化后完全/高度相同 | material 交 Critic 确认 |
| 无效开场白 | 开头 1-2 段的高频填充语匹配（中英文） | minor/material |
| 无效结束语 | 结尾客套语匹配 | minor |
| 结构异常 | 段落数、列表项数量、单句段落比例等辅助信号 | info/minor，只作风险信号 |
| 字数异常 | 仅作为风险信号 | info，永不单独决定拦截 |

实现注意：

- 忽略代码块（三反引号围栏与围栏内内容）中的重复；代码块先整体剔除再分析。
- 不要把 Markdown 表格、引用、正常列表误认为重复。
- 不使用固定字数上限直接拦截。
- 相似引用可能是合理引用：规则发现只作为 Critic 的输入与日志，不单独输出 BLOCK。
- 规则输出的 `Evidence` 必须截断（≤ 120 字符），不得携带完整回答。
- 阈值常量集中在 `checker.go`，便于回归调参。

## 8. LLM Critic 要求

- 协议：OpenAI-compatible `POST {base_url}/chat/completions`，温度 0，不重试。
- 只判断，不重写。
- 输入：回答全文、可选的最近一条用户消息（≤ 2000 字符）、规则 findings 摘要。
- 无法获得用户问题时，不得仅凭篇幅长判定不合格。
- 输出必须严格解析为：

```json
{
  "decision": "block",
  "severity": "material",
  "issues": [
    {"type": "semantic_duplicate", "location": "paragraph_3", "instruction": "与第一段观点重复，应合并"}
  ]
}
```

- `decision` 取值 `pass|block`；`severity` 取值 `none|minor|material`。
- 非法 JSON、未知枚举、非 2xx、超时（默认 8s）→ 返回错误 → FAIL-OPEN。
- 记录 latency 与 token usage（不记录回答全文、不记录 API Key）。
- 回答与必要用户上下文会发送给配置的 Critic 服务；README 必须写明该数据处理边界。

### 8.1 模式

| 模式 | 行为 |
| --- | --- |
| `balanced`（默认） | 非短小自然语言回答（natural_runes ≥ 120）全部送 Critic；任何规则发现也送 Critic |
| `economy` | 只有规则发现可疑时才调用 Critic |

短回答（自然语言不足 120 runes）且规则无发现时不调用 Critic，直接放行；
短回答只要出现规则发现（哪怕只是 minor 填充语）仍会送 Critic，因为规则本身从不产生 BLOCK。

## 9. 最终决策（policy）

- Critic 返回 `decision=block && severity=material` → BLOCK，reason 使用固定可执行指令。
- Critic 返回 pass/minor → PASS。
- Critic 调用失败 / 未配置 → FAIL-OPEN PASS，日志 `fail_open=true`。
- 规则 findings 单独不产生 BLOCK（重复内容可能是合理引用，必须交给 Critic 确认）。
- 重写 reason（固定文案）：

```text
请重新输出完整最终回答：合并语义重复的段落，删除无效铺垫和结论重复，保留全部必要事实、数字、代码和解释，不要为了缩短篇幅而删除必要信息。
```

## 10. 运行参数与环境变量

| 参数 | 默认值 | 环境变量 |
| --- | --- | --- |
| Hook 超时（hooks.json） | 30 秒 | — |
| Hook 进程总预算 | 25 秒 | `QUALITY_GATE_TOTAL_TIMEOUT_MS` |
| Critic HTTP 超时 | 8 秒 | `QUALITY_GATE_CRITIC_TIMEOUT_MS` |
| Critic 温度 | 0 | — |
| 最大重写次数 | 1 | — |
| Critic 重试 | 0 | — |
| Hook 异常策略 | FAIL-OPEN | — |
| Critic Base URL | 空 | `QUALITY_GATE_BASE_URL` |
| Critic API Key | 空 | `QUALITY_GATE_API_KEY` |
| Critic 模型 | 空 | `QUALITY_GATE_MODEL` |
| 模式 | balanced | `QUALITY_GATE_MODE` |
| 最小 Critic 字数 | 120 | `QUALITY_GATE_MIN_CRITIC_RUNES` |

API Key 不写入仓库、不写入日志。

## 11. 日志与监控

每次审查 stderr 输出一行结构化 JSON：

```json
{
  "ts": "2026-11-02T13:04:05.000Z",
  "level": "info",
  "event": "quality_check",
  "mode": "balanced",
  "decision": "block",
  "reason_type": "semantic_duplicate",
  "rule_types": ["duplicate_sentence"],
  "rule_material": true,
  "answer_runes": 812,
  "natural_runes": 764,
  "critic_called": true,
  "critic_latency_ms": 1250,
  "critic_tokens": 320,
  "fail_open": false,
  "elapsed_ms": 1400
}
```

不记录完整回答、API Key、不必要的用户隐私。

核心指标：规则误拦截率、Critic 一致性、最终重写改善率、FAIL-OPEN 比例、平均审查延迟、总 Token 消耗。
总 Token 成本 = 原始回答 + Critic + 重写回答，不能只用 Hook 自身 Critic 用量代替。

## 12. 回归测试体系

### 12.1 30 条样本（testdata/cases/*.jsonl）

`positive.jsonl`：20 条有问题的回答（预期 block），每类 5 条：
`duplicate_conclusion`、`filler_opening`、`over_fragmentation`、`redundant_explanation`。

`negative.jsonl`：10 条正常回答（预期 pass）：
`good_long` 5 条（正常长篇技术回答）、`good_short` 5 条（简短合格回答）。

每行一个 JSON 对象：

```json
{
  "id": "dup-conclusion-01",
  "category": "duplicate_conclusion",
  "user_message": "用户原始问题（用于给 Critic 提供上下文，可为空字符串）",
  "answer": "完整回答 Markdown；换行用 JSON 转义序列",
  "expected": "block",
  "expected_severity": "material",
  "expected_rule_signals": ["duplicate_paragraph"],
  "notes": "样本要考察的具体质量缺陷"
}
```

要求：

- 中文、真实 Codex 风格回答；问题样本每类 5 条且各自只突出一个主要缺陷。
- 问题样本长度 60–600 字符（over_fragmentation 可稍长），good_long 600–1500 字符。
- 重复结论样本应能触发重复句子/段落规则；无效铺垫样本应能触发 filler 规则；
  纯语义类别（over_fragmentation / redundant_explanation）允许 expected_rule_signals 为空。
- 正常样本必须 expected_rule_signals 为空，且规则层不得出现 material finding。
- JSONL 一行一个对象、UTF-8、无 BOM、可被 encoding/json 解析。

### 12.2 第一层：规则测试

对 30 条样本各执行一次规则层，统计：规则覆盖率（有问题样本中规则信号非空的占比）、
误杀率（正常样本中 material finding 的比例）、漏检率。good 样本误杀必须为 0。

### 12.3 第二层：Critic 测试

每条样本重复 3 次取多数，记录判断一致性；无真实 API Key 时使用本地 mock server 验证协议与
fail-open，并在报告中说明真实 Critic 一致性需配置服务后补测。

### 12.4 Hook 集成测试（必做）

| 场景 | 预期 |
| --- | --- |
| 正常回答 | `{}` |
| 第一次发现严重问题 | block |
| 重写后的第二次 Stop（stop_hook_active=true） | `{}` |
| stop_hook_active 缺失 | 放行并告警 |
| Hook 输入解析失败 | 放行并记录异常 |
| last_assistant_message 为空 | 放行 |
| Critic 超时 | 放行并记录异常 |
| Critic 返回非法 JSON | 放行并记录异常 |
| stdout | 永远是合法 JSON |

### 12.5 超时验证（必做）

临时把 hooks.json 的 `timeout` 改为 2，Hook 命令改为一个 sleep 5 的命令，确认 Codex 约 2 秒
终止 Hook，然后恢复 30 秒，并记录 `codex --version`。

## 13. 后续扩展路线

1. Phase 1（当前）：Codex Stop Hook + Go Checker + Critic + 一次重写限制 + 回归测试。
2. Phase 2：`crisp check-file README.md` 文档检查（Stop Hook 不自动检查磁盘文件）。
3. Phase 3：DeepSeek Harness（Cordis 插件接入 `llm/stream`；规则可流式，语义需缓存完整回答）。
4. Phase 4：通用适配层；对完全不支持扩展的 Agent 再考虑本地 API Proxy。

## 14. CLI

| 命令 | 用途 |
| --- | --- |
| `crisp` / `crisp hook` | Codex Stop Hook 模式：stdin 读 hook JSON，stdout 写决策 JSON |
| `crisp check-text` | 测试/评测入口：stdin 读原始回答文本，stdout 输出决策摘要 JSON |
| `crisp version` | 版本 |
