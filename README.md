# Crisp

**Agent 输出质量门禁**：在 Agent 回答交付用户之前检查质量，拦截重复表达、无效铺垫、过度拆分、
无效解释、结论重复和信息密度低等问题——但绝不为了精简而丢失事实、数字、代码和必要解释。

*Crisp is a fail-open output-quality gate for terminal coding agents: deterministic rules plus an
OpenAI-compatible LLM critic, with at most one full rewrite per turn. In the DeepSeek Harness plugin,
`delivery: optimize` can rewrite a material answer **before delivery** and stream only the rewritten
answer to the user, with no extra visible turn.*

[![CI](https://github.com/XiaoleC05/Crisp/actions/workflows/ci.yml/badge.svg)](https://github.com/XiaoleC05/Crisp/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.26%2B-00ADD8?logo=go)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

## 为什么需要 Crisp

Agent 的回答经常"看起来很多，实际信息很少"：开头客套、结尾客套、同一结论换三种说法、
一句话拆成八个小标题、把用户已知的基础知识又解释一遍。Crisp 的检查核心只做一件事：
**在回答交付前判断是否存在明确的实质性质量缺陷**，如果有，要求 Agent 重新生成完整回答。

核心取舍（也是这个项目的设计原则）：

| 原则 | 含义 |
| --- | --- |
| 默认放行 | 正常回答、长篇技术回答必须能通过；不按字数拦截 |
| 规则从不单独 BLOCK | 重复文本可能是合理引用，确定性规则只作为 Critic 的输入 |
| 一次重写 | 每个 Turn/消息最多触发一次完整重写，避免无限循环与成本失控 |
| FAIL-OPEN | 解析失败、超时、网络错误、非法 JSON、未配置 Critic 都放行，绝不拦截正常回答 |
| 不删除内容 | 只返回可执行的重写指令，让 Agent 重新生成；不改写、不截断、不删除原文 |
| 结论先行、结构化 | 默认评审偏好：先给直接结论再展开；结构服务于可读性，短回答不强制分节 |
| 可解释 | 每次审查输出结构化日志：决策、原因类型、规则信号、耗时、token 数 |

## 支持的 Agent

| Agent | 接入方式 | 拦截时点 | 防循环机制 | 验证状态 | 文档 |
| --- | --- | --- | --- | --- | --- |
| **Codex CLI** | Stop Hook | 交付前：block 后完整重写 | `stop_hook_active` | 8 项检查通过（S0 replay + S1–S6 真实 CLI） | [codex-cli](docs/integrations/codex-cli.md) |
| **Claude Code** | Stop Hook | 交付前：block 后完整重写 | `stop_hook_active` | 3 项检查通过（含真实 CLI 场景） | [claude-code](docs/integrations/claude-code.md) |
| **DeepSeek Harness** | Cordis Host + Web 插件（`llm/stream` + 左侧栏页面） | 默认 observe；guard 可后置重写或交付前优化（`delivery: optimize`） | 插件内存 guard / 单次 optimizer | 109 单测 + 仓库内 headless guard E2E；本机实测左侧栏、真实 Critic 与交付前重写 | [deepseek-harness](docs/integrations/deepseek-harness.md) |
| **opencode** | 插件事件钩子 | 后置：toast + 一次重写 followup | 插件内存 guard | 51 单测 + 真实 serve E2E | [opencode](docs/integrations/opencode.md) |
| 其他 Hook Agent | 通用 stdin/stdout 协议 | 由宿主是否阻塞决定 | 宿主 + `stop_hook_active` | 协议文档 | [generic-hook-protocol](docs/integrations/generic-hook-protocol.md) |

选择哪种接入方式见 [docs/integrations/README.md](docs/integrations/README.md)。

## 快速开始

### 1. 构建

需要 Go 1.26+；核心检查器零第三方依赖。插件接入需要 Node.js 20+（`node:test`，无 npm 依赖）。

```powershell
go build -o bin/crisp.exe ./cmd/crisp
go test ./...
```

### 2. 配置 Critic

Critic 使用 OpenAI-compatible Chat Completions 接口：

```powershell
$env:QUALITY_GATE_BASE_URL = "https://api.example.com/v1"
$env:QUALITY_GATE_API_KEY  = "sk-..."       # 可选，本地服务可留空；不要提交到仓库
$env:QUALITY_GATE_MODEL    = "your-light-model"
$env:QUALITY_GATE_MODE     = "balanced"     # 推荐；economy 只在规则可疑时调用 Critic
```

未配置 Critic 时整体 fail-open：只做本地规则检查、不拦截。生产使用应配置。

### 3. 安装接入

```powershell
# Codex CLI（项目级 .codex/hooks.json）
pwsh -File scripts/install-codex-hook.ps1

# Claude Code（项目级 .claude/settings.json）
pwsh -File scripts/install-claude-hook.ps1
```

- Codex 只执行受信任的 Hook：交互式首次会提示审查；自动化 `codex exec` 需要
  `--dangerously-bypass-hook-trust`。字段名必须是 `timeout`（不是 `timeout_sec`/`timeoutSec`）。
- Claude Code 需要工作区信任；若用户级设置里 `disableAllHooks: true`，项目级 Hook 不会触发。
- DeepSeek Harness / opencode 的安装在各自接入文档中；插件均为本仓库内私有包，未发布 registry。

### 4. 验证运行

```powershell
# 直接模拟一次 Stop hook 输入（放行）
'{"session_id":"s","turn_id":"t","stop_hook_active":false,"last_assistant_message":"这是一段足够长的正常回答。"}' | .\bin\crisp.exe

# 真实 CLI E2E（脚本会查找 CLI；找不到时输出 SKIPPED 并以 0 退出；不修改用户配置）
pwsh -File scripts/e2e-codex.ps1
pwsh -File scripts/e2e-claude.ps1
```

### 5. 单文件检查与文档检查

```powershell
# 检查一段原始回答文本（测试/评测入口，输出决策摘要 JSON）
Get-Content answer.md -Raw | .\bin\crisp.exe check-text

# 检查一个或多个文件：支持 --json 与 stdin "-"，退出码 0 通过 / 1 BLOCK / 2 错误
.\bin\crisp.exe check-file README.md docs/design.md
.\bin\crisp.exe check-file --json README.md
```

`check-file` 对每个文件运行同一检查流水线；文件读取失败按 fail-open 记录并继续检查其他文件。
完整 CLI 说明见 `crisp help` 与 [docs/design.md §14](docs/design.md)。

## 工作方式

```text
Agent 生成完整回答
      │
      ▼
Stop Hook / 插件钩子 ──────────▶ crisp
      │                          1. Deterministic Checker（不调用大模型）
      │                          2. LLM Critic（balanced / economy）
      │                          3. Quality Decision
      ▼
stdout: {} 放行     或     {"decision":"block","reason":"..."} 要求完整重写
```

| 决策 | 行为 |
| --- | --- |
| PASS | 输出 `{}`，放行原始回答 |
| BLOCK | 返回 `decision:block`，宿主重新生成完整回答（每 Turn 最多一次） |
| FAIL-OPEN | 任何内部异常都输出 `{}` 并记录日志，绝不拦截 |

组件、数据流、fail-open 矩阵、超时预算与扩展方式见 [docs/architecture.md](docs/architecture.md)。

## 配置参考

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `QUALITY_GATE_BASE_URL` | 空 | Critic OpenAI-compatible Base URL |
| `QUALITY_GATE_API_KEY` | 空 | 只在请求头中使用；不落日志、不入库 |
| `QUALITY_GATE_MODEL` | 空 | Critic 模型名 |
| `QUALITY_GATE_MODE` | `balanced` | `balanced` 或 `economy` |
| `QUALITY_GATE_CRITIC_TIMEOUT_MS` | 8000 | Critic HTTP 超时 |
| `QUALITY_GATE_MIN_CRITIC_RUNES` | 120 | 短于该自然语言长度的回答不调用 Critic |
| `QUALITY_GATE_EXTRA_BODY_JSON` | 空 | 可选：合并进 Critic 请求体的 JSON 对象（如 `{"thinking":{"type":"disabled"}}` 关闭思考模式） |
| `QUALITY_GATE_TOTAL_TIMEOUT_MS` | 25000 | 单次 Hook 进程总预算（应小于宿主 timeout） |

宿主侧还有各自配置（hooks.json 的 `timeout`、插件 `mode`/`timeoutMs`/`rewrite` 等），见接入文档。

## 验证与质量数据

| 验证项 | 结果 | 证据 |
| --- | --- | --- |
| 30 条回归样本（20 问题 / 10 正常） | 正常样本零误杀（0/10），规则目标类别 15/15 命中 | [eval-report](docs/eval-report.md) |
| 规则层性能 | 平均 208 µs/条，最大 552 µs（30 条样本） | [eval-report](docs/eval-report.md) |
| Hook 集成测试 | 正常/block/二次 Stop/字段缺失/解析失败/空消息/Critic 故障/stdout JSON | `go test ./cmd/crisp` |
| Codex CLI 0.145.0-alpha.18 | 真实 E2E 8/8（含 timeout、trust gate、loop guard） | [codex-cli](docs/integrations/codex-cli.md) |
| Claude Code 2.1.312 | 真实 E2E 3/3（block → 重写 → 二次 Stop 放行） | [claude-code](docs/integrations/claude-code.md) |
| DeepSeek Harness 插件 | 109 单测 + 仓库内真实 dsh headless guard E2E；本机另测 optimize 交付前重写 | [deepseek-harness](docs/integrations/deepseek-harness.md) |
| DeepSeek Harness Web 左侧栏 | 实机验证：左栏「输出优化」入口打开独立状态页，展示 GUARD、deepseek-flash、思考关闭、今日计数与最近记录；页面无 console 错误 | [截图](docs/assets/deepseek-harness-sidebar.png) / [deepseek-harness](docs/integrations/deepseek-harness.md) |
| 真实 Critic（DeepSeek-V4.1-Flash HTTP，思考关闭） | 冒烟：重复结论样本 block/semantic_duplicate（1.4s，1167 tokens），正常长样本 pass（0.6s，1272 tokens），`fail_open=false` | [deepseek-harness](docs/integrations/deepseek-harness.md) |
| DSH 交付前重写（实机，`delivery: optimize`） | 受控无效铺垫样本：BLOCK(`filler`) → 交付前重写 258→120 runes（540 tokens）；用户只收到优化版，无额外 turn | [deepseek-harness](docs/integrations/deepseek-harness.md) |
| opencode 1.18.18 插件 | 51 单测 + 真实 serve E2E（pass 与 block→一次重写） | [opencode](docs/integrations/opencode.md) |

已提交的 E2E 使用本地 mock（宿主安装/依赖步骤可能联网），不使用用户凭据、不修改用户配置；
上表的"真实 Critic"、"Web 左侧栏"和"DSH 交付前重写"是在本机集成环境中的额外实测，不属于仓库内可重复的 mock E2E。
**尚未测量**：真实 Critic 的 30 样本 × 多次一致性、误杀率与最终重写改善率——需要按
[eval-report](docs/eval-report.md) 的方法补测。

## 数据边界与安全

- 回答全文与可选的最近一条用户消息（截断到 2000 字符）会发送给配置的 Critic 服务；
  `delivery: optimize` 命中重写时，原回答还会发送给已登录的 Harness 模型生成一次重写；
  请只配置可信服务，是否外发由部署方决定。
- 本地日志只记录决策、规则类型、耗时、token 数等指标，**不记录完整回答、用户隐私或 API Key**。
- Hook 只执行本地 `crisp` 进程；插件通过 stdin 调用同一二进制；Crisp 不监听网络端口、不改写文件。
- 安全模型、漏洞报告与加固建议见 [SECURITY.md](SECURITY.md)。

## 文档

| 文档 | 内容 |
| --- | --- |
| [docs/design.md](docs/design.md) | 实施规格：目标、硬约束、协议、规则/Critic/决策、测试体系 |
| [docs/architecture.md](docs/architecture.md) | 架构、数据流、fail-open 矩阵、超时、可观测性、扩展指南、取舍 |
| [docs/eval-report.md](docs/eval-report.md) | 评测方法、结果、复现命令与局限 |
| [docs/troubleshooting.md](docs/troubleshooting.md) | 按症状排查问题 |
| [docs/integrations/](docs/integrations/README.md) | 各 Agent 接入指南与通用 Hook 协议 |
| [CHANGELOG.md](CHANGELOG.md) | 版本变更记录 |
| [CONTRIBUTING.md](CONTRIBUTING.md) | 开发环境、核心不变量、测试与提交规范 |

## 仓库结构

```text
cmd/crisp/                       进程入口：Stop Hook / check-text / check-file
internal/                        Go 检查核心（hook/checker/critic/policy/logger）
integrations/dsh-plugin-crisp/   DeepSeek Harness（Cordis）Host 插件
integrations/opencode-crisp/     opencode 插件
testdata/cases/                  30 条回归样本
test/e2e/                        真实 CLI E2E 驱动、mock 服务与协议 replay
test/docscheck/                  Markdown 相对链接检查（CI 使用）
scripts/                         构建 / Hook 安装 / E2E 脚本
.codex/ .claude/                 项目级 Hook 配置
docs/                            文档中心（见 docs/README.md）
```

## 测试

```powershell
go vet ./...
go test ./...                                   # 单测 + Hook 集成测试
go test ./cmd/crisp -run TestRuleEvaluation -v  # 30 条样本规则层评测
go run ./test/docscheck                         # 文档链接检查
go run ./test/e2e/hookreplay -crisp bin/crisp.exe

# 插件单元测试（Node.js 20+）
Push-Location integrations/dsh-plugin-crisp; node --test; Pop-Location
Push-Location integrations/opencode-crisp; node --test; Pop-Location
```

CI（GitHub Actions）在 Linux/Windows 上运行 Go 检查、协议 replay、文档链接检查与插件单测。

## 已知限制

- **插件宿主没有交付前 block 协议**：DeepSeek Harness 与 opencode 只能 observe + 后置一次重写
  （DSH 默认 observe；opencode 默认 rewrite，会多一轮可见对话与模型调用）。
- **规则层不覆盖纯语义缺陷**：明显重复/填充语可被规则发现；无效解释、无明显重复句的语义重复
  依赖 Critic。economy 模式会漏检纯语义类别，默认推荐 balanced。
- **真实 Critic 效果待补测**：当前 E2E 使用 mock 验证协议与循环，不评估模型质量。
- **Windows Codex timeout 语义**：超时只丢弃 Hook 输出、不终止 Hook 进程；Crisp 自身 25s 预算负责退出。
- **未配置 Critic 时不拦截**；插件包未发布到 npm/pnpm registry；本地 API Proxy 尚未实现。
- 每个 Turn 最多重写一次：阻塞 Hook 由 `stop_hook_active` 保证；插件由内存 guard 保证，重启会重置。

## Roadmap

| 阶段 | 状态 | 内容 |
| --- | --- | --- |
| Phase 1 | 完成 | Codex Stop Hook + Go Checker + Critic + 一次重写限制 + 回归测试 |
| Phase 2 | 完成 | `crisp check-file` 文档检查 |
| Phase 3 | 完成 | DeepSeek Harness Cordis 插件（observe/guard） |
| Phase 4 | 完成 | Claude Code / opencode 接入 + 通用 Hook 协议文档 |
| Next | 计划 | 真实 Critic 一致性与阈值调参；本地 API Proxy（仅对无法扩展的 Agent） |

## 贡献与许可

- 贡献方式、核心不变量与 PR 清单见 [CONTRIBUTING.md](CONTRIBUTING.md)。
- 安全问题请按 [SECURITY.md](SECURITY.md) 私下报告。
- 许可证：[MIT](LICENSE)。版本记录见 [CHANGELOG.md](CHANGELOG.md)。
