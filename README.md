# Crisp

Agent 输出质量门禁（Quality Gate）：在 Agent 回答交付用户之前自动检查回答质量，
只拦截存在明确实质问题的回答，正常回答默认放行。

第一版（v0.1 MVP）只接入 **Codex CLI Stop Hook**；检查核心与 Agent 框架解耦，后续再扩展接入层。

- 二进制：`crisp`
- 设计规格：[docs/design.md](docs/design.md)
- Codex 接入验证：[docs/codex-e2e.md](docs/codex-e2e.md)
- 规则评测报告：[docs/eval-report.md](docs/eval-report.md)

## 验证状态

- 30 条回归样本：规则层零误杀（0/10 正常样本），规则目标类别 15/15 命中；纯语义类别交给 Critic。
- Codex CLI `0.145.0-alpha.18` 真实 Stop Hook E2E：8/8 场景通过，包含
  pass、block → 完整重写 → 二次 Stop 放行（每个 Turn 最多一次重写）、timeout、trust gate。
- 已知 Windows 差异：hooks.json 的 `timeout` 是结果截止时间，超时后 Codex 丢弃 Hook 输出
  （CLI 层 fail-open），但不会终止 Hook 进程；Crisp 自身的 25 秒总预算保证正常提前退出。

## 解决什么问题

1. 重复表达：同一观点换不同说法反复出现。
2. 无效铺垫：客套话、背景复述、没有信息量的开场白。
3. 过度拆分：一句话能说清的问题拆成多个小点。
4. 无效解释：反复解释用户已经知道的内容。
5. 结论重复：开头、正文、结尾重复同一结论。
6. 信息密度低：文字多，新增有效信息少。

## 工作方式

```text
Codex 生成完整回答
      │
      ▼
Stop Hook（.codex/hooks.json）──stdin──▶ crisp
      │                                  1. 确定性规则检查（不调用大模型）
      │                                  2. LLM Critic 语义审查（balanced/economy）
      │                                  3. 统一决策 PASS / BLOCK / FAIL-OPEN
      ▼
stdout: {} 放行        或        {"decision":"block","reason":"..."} 要求完整重写
```

决策规则：

| 结果 | 行为 |
| --- | --- |
| PASS | 输出 `{}`，放行原始回答 |
| BLOCK | 返回 `decision:block`，Codex 重新生成**完整**回答（每个 Turn 最多一次） |
| FAIL-OPEN | 检查系统任何异常（解析失败、超时、网络错误、非法 JSON）都输出 `{}`，绝不拦截正常回答 |

## 构建

需要 Go 1.26+，无第三方依赖。

```powershell
go build -o bin/crisp.exe ./cmd/crisp
go test ./...
```

## 接入 Codex CLI

### 1. 配置 Critic 服务

Critic 使用 OpenAI-compatible Chat Completions 接口：

```powershell
$env:QUALITY_GATE_BASE_URL = "https://api.example.com/v1"
$env:QUALITY_GATE_API_KEY  = "sk-..."       # 可选，本地服务可留空；不要提交到仓库
$env:QUALITY_GATE_MODEL    = "your-light-model"
$env:QUALITY_GATE_MODE     = "balanced"     # 推荐 balanced；economy 只在规则可疑时调用 Critic
```

未配置 Critic 时质量门禁整体 fail-open（只记录日志，不会拦截），因此生产使用必须配置。

### 2. 安装 Stop Hook

把下面的绝对路径换成你的 `crisp.exe` 路径，写入项目级 `.codex/hooks.json`：

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

也可以用安装脚本生成：

```powershell
powershell -ExecutionPolicy Bypass -File scripts/install-codex-hook.ps1
```

**字段名必须是 `timeout`**（单位秒）。`timeout_sec` 是 Rust 内部字段，`timeoutSec` 是 App Server
序列化字段，都不是 hooks.json 字段；写错可能被静默忽略并回退到 600 秒。

**Hook 信任**：Codex 只执行被信任的 Hook。交互式 TUI 首次运行会提示审查/信任 Hook；
自动化运行 `codex exec` 需要加 `--dangerously-bypass-hook-trust`（仅用于已确认 Hook 来源的自动化），
否则 Hook 不会执行。详见 [docs/codex-e2e.md](docs/codex-e2e.md)。

### 3. 验证

```powershell
# 直接模拟一次 Stop hook 输入（放行场景）
'{"session_id":"s","turn_id":"t","stop_hook_active":false,"last_assistant_message":"这是一段足够长的正常回答。"}' | .\bin\crisp.exe

# 超时字段验证见 docs/codex-e2e.md
`~

## 文档检查（Phase 2）

Stop Hook 只检查 Agent 的最终回答；对磁盘上生成或已有的 Markdown、文本文件，可以用同一条
Checker + Critic + 决策流水线显式检查：

`~powershell
.\bin\crisp.exe check-file README.md
.\bin\crisp.exe check-file --json docs/design.md docs/eval-report.md
Get-Content README.md -Raw | .\bin\crisp.exe check-file -
`~

人读模式输出示例：

`~text
README.md: PASS (reason_type=critic_pass; rules=length_signal; critic=pass; runes=5430; 812ms)
checked=1 blocked=0 fail_open=0 errors=0
`~

`--json` 输出单个对象 `{"files":[...],"summary":{...}}`。退出码：`0` 全部通过（含
fail-open）、`1` 至少一个文件判定 BLOCK、`2` 用法错误或存在无法读取的文件（读取失败的
文件按 fail-open 记录，其他文件继续检查）。每个文件最多读取 4 MiB，超限按读取失败处理。
未配置 Critic 时与 Hook 模式一致，只做规则检查并 fail-open。

## 配置项

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `QUALITY_GATE_BASE_URL` | 空 | Critic OpenAI-compatible Base URL |
| `QUALITY_GATE_API_KEY` | 空 | Critic API Key，只在请求头中使用，不落日志、不入库 |
| `QUALITY_GATE_MODEL` | 空 | Critic 模型名 |
| `QUALITY_GATE_MODE` | `balanced` | `balanced` 或 `economy` |
| `QUALITY_GATE_CRITIC_TIMEOUT_MS` | 8000 | Critic HTTP 超时 |
| `QUALITY_GATE_MIN_CRITIC_RUNES` | 120 | 短于该自然语言长度的回答不调用 Critic |
| `QUALITY_GATE_TOTAL_TIMEOUT_MS` | 25000 | Hook 进程总预算（必须小于 hooks.json 的 timeout） |

## 数据处理边界

- 回答全文、可选的最近一条用户消息（截断到 2000 字符）会发送给配置的 Critic 服务；
  是否允许外发取决于你选择的 Critic 服务与合规要求。
- 本地日志（stderr）只记录决策、规则类型、耗时、token 数等结构化指标，
  **不记录完整回答、用户隐私或 API Key**。
- Codex 自身的会话用量请从 Codex 侧获取；总 Token 成本 = 原始回答 + Critic + 重写回答，
  不能只用 Hook 自身 Critic 用量代替。

## 测试

```powershell
go vet ./...
go test ./...                       # 单元测试 + Hook 集成测试
powershell -File scripts/e2e-codex.ps1   # Codex CLI 真实接入 E2E（见 docs/codex-e2e.md）

go test ./cmd/crisp -run TestRuleEvaluation -v   # 30 条样本规则层评测
```

规则层评测结果见 [docs/eval-report.md](docs/eval-report.md)。
```

回归样本位于 `testdata/cases/`：20 条问题回答（重复结论 / 无效铺垫 / 过度拆分 / 无效解释）
与 10 条正常回答（长篇技术回答 / 简短合格回答），说明见 `testdata/cases/README.md`。

## 监控

每次审查向 stderr 输出一行结构化 JSON，示例：

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

核心指标：规则误拦截率、Critic 一致性、最终重写改善率、FAIL-OPEN 比例、平均审查延迟、总 Token 消耗。

## 目录结构

```text
cmd/crisp/            进程入口与 Hook 流程编排
internal/hook/        Codex Stop Hook 协议解析 / 输出
internal/checker/     确定性规则检查
internal/critic/      LLM Critic 客户端、prompt、schema
internal/policy/      统一决策与重写指令
internal/logger/      stderr 结构化日志
testdata/cases/       30 条回归样本
test/e2e/ Codex CLI 真实接入 E2E 与 mock 服务
scripts/              构建 / Hook 安装 / E2E 脚本
docs/                 设计规格、E2E 记录、评测报告
```

## 已知限制

- 目前只支持 Codex CLI Stop Hook；其他 Agent 在后续 Phase 接入。
- Stop Hook 不会自动检查磁盘上生成的文件；需要显式运行 `crisp check-file`（Phase 2 已实现）。
- 每个 Turn 最多触发一次完整重写；重写后若仍有问题，本 Turn 不再拦截。
- 未配置 Critic 时只做规则检查并 fail-open（不拦截）。

## Roadmap

1. Phase 1（已完成）：Codex Stop Hook + Go Checker + LLM Critic + 一次重写限制 + 回归测试。
2. Phase 2（已完成）：`crisp check-file` 文档检查。
3. Phase 3（进行中）：DeepSeek Harness（Cordis 插件接入 `llm/stream`）。
4. Phase 4（进行中）：终端 Agent 接入层（Codex / Claude Code / opencode）；对不支持扩展的 Agent 再考虑本地 API Proxy。
