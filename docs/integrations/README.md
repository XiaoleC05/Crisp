# Agent 接入总览

> 文档中的 Windows 绝对路径（如 ~~D:\05_Code\Crisp~~）与本机运行编号（如 ~~run-20261102-...~~）来自验证环境，
> 请替换为你自己的路径；~~.tmp/~~ 引用是 gitignored 的本地证据目录，不会随仓库发布。

Crisp 的检查核心（确定性规则 + LLM Critic + 统一决策）与 Agent 框架解耦，接入层只负责
"什么时候把回答交给 crisp、拿到决策后做什么"。本目录收录当前已实现的四类接入与一份通用协议：

- 接入前先理解共同约束：[一次重写、fail-open、数据边界](#3-共同前置条件)；
- 新宿主请直接看 [通用 Stop-hook 协议](generic-hook-protocol.md) 的适配清单；
- 每篇接入文档都显式区分 **已验证 / 未验证**，本文只做索引与选择建议。

---

## 1. 接入矩阵

| Agent | 接入方式（入口） | 拦截时点 | 防循环机制 | 验证状态 | 文档 |
| --- | --- | --- | --- | --- | --- |
| **Codex CLI** `0.145.0-alpha.18` | Stop Hook：`.codex/hooks.json`，stdin/stdout JSON | **交付前**：block 后 CLI 完整重新生成回答 | `stop_hook_active`（CLI 在重写后的 Stop 置 `true`） | 真实 CLI E2E **8/8**（`scripts/e2e-codex.ps1`，run-20261102-215036，含 S0 replay 12/12） | [codex-cli.md](codex-cli.md) |
| **Claude Code** `2.1.312` | Stop Hook：`.claude/settings.json`，与 Codex 同一协议 | **交付前**：同上 | `stop_hook_active`（同上） | 真实 CLI E2E **3/3**（`scripts/e2e-claude.ps1`，run-20261102-221324）；CLI 缺失时 SKIPPED + exit 0 | [claude-code.md](claude-code.md) |
| **DeepSeek Harness** `0.2.1-rc.5` | Cordis Host 插件：`llm/stream` waterfall | **后置**：默认 observe 只记录；guard 在一次 block 后追加 **一次** 重写 turn（原回答已流式交付，无法撤回） | 插件内存 guard：按 durable turn id 去重，重写 turn 不再审查 | **57** 个 `node:test` 全过 + 真实 `dsh` headless guard E2E（隔离 `DSH_HOME`） | [deepseek-harness.md](deepseek-harness.md) |
| **opencode** `1.18.18` | opencode 插件：`event` 钩子（`session.idle` 等） | **后置**：toast 提示 + 默认追加一次重写 turn | 插件内存 guard：`checked` / `pendingRewrite` / `rewrittenTurns` | **51** 个 `node:test` 全过 + 真实 `opencode serve` E2E | [opencode.md](opencode.md) |
| **其他 Hook Agent** | 通用 stdin/stdout 协议（阻塞 command hook 优先） | 取决于宿主：阻塞式 = 交付前；async/纯事后钩子 = 后置 | 宿主提供 `stop_hook_active`，或适配器自行按 turn 追踪 | 协议由 Codex/Claude 真实验证；新宿主按 checklist 自测 | [generic-hook-protocol.md](generic-hook-protocol.md) |

说明：

- 四类接入的 E2E 都用**本地 mock** provider/critic，验证的是协议、循环守卫与失败路径，
  不是真实模型的判定质量；真实 Critic 一致性与重写改善率仍未测量（见 [评测报告](../eval-report.md)）。
- 两个插件包是**本地私有包**（`package.json` 中 `"private": true`），未发布到 npm/pnpm registry，
  安装使用本仓库路径，见各接入文档。
- 除特别标注外，所有实测都在 Windows 上完成；其他平台未验证。

---

## 2. 如何选择接入方式

按宿主能力从强到弱选择，**不要为了"拦截"去自行缓冲整段回答**（会牺牲首 token 延迟，且已渲染内容
也无法撤回）：

1. **宿主有阻塞式 turn-end / Stop 钩子，且能消费 `{"decision":"block"}`** →
   直接接 Stop Hook（Codex CLI / Claude Code 模式）。这是最佳形态：**交付前拦截**、一次完整重写、
   不产生额外的可见轮次、成本最低。
2. **宿主只有插件 API，回答先流式显示（DeepSeek Harness / opencode）** →
   只能做**后置**检查：先用 `observe` 收集误报率与成本，需要时再开 `guard`（多一次模型调用与可见
   重写 turn）。无法阻止原始回答显示，也无法真正"撤回"。
3. **宿主只有 async / 纯事后钩子** → 同样只能后置修正（toast、追加 turn、日志审计）；
   能否自动化重写取决于宿主是否提供追加输入的能力。
4. **宿主完全没有扩展点** → 用 `crisp check-text` / `crisp check-file` 做离线或 CI 检查；
   本地 API Proxy 属于后续工作（设计规格中的"最后才考虑"项）。
5. **不确定时** → 先只上 `observe`（DSH/opencode 默认就是 observe），或让 Stop Hook 先不配置 Critic
   （只跑规则、永不拦截）；观察一段时间日志中的 `decision`、`critic_called`、`fail_open` 后再决定
   是否启用重写。

新增接入前先读 [通用协议](generic-hook-protocol.md)：它定义了最小输入字段、输出契约、
fail-open 不变量与「每个 Turn 最多一次重写」的适配要求。

---

## 3. 共同前置条件

### 3.1 构建与运行时

```powershell
# 构建 Crisp 二进制（Go 1.26+，核心零第三方依赖）
go build -o bin/crisp.exe ./cmd/crisp
# 或
pwsh -File scripts/build.ps1
```

- 插件接入需要 **Node.js >= 20**（两个插件 manifest 的 `engines.node` 均为 `>=20`），插件本身不引入
  npm 依赖，单测使用内置 `node:test`。
- Windows 上的 Hook 脚本需要 PowerShell 7+（`#Requires -Version 7.0`）。

### 3.2 Critic 配置

Critic 使用 OpenAI-compatible Chat Completions 接口（`POST {base_url}/chat/completions`）：

| 环境变量 | 默认值 | 说明 |
| --- | --- | --- |
| `QUALITY_GATE_BASE_URL` | 空 | Critic Base URL；为空时 Critic 不可用 |
| `QUALITY_GATE_API_KEY` | 空 | 可选；只在请求头中使用，不落日志、不入库 |
| `QUALITY_GATE_MODEL` | 空 | Critic 模型名 |
| `QUALITY_GATE_MODE` | `balanced` | `balanced`（默认）或 `economy` |
| `QUALITY_GATE_CRITIC_TIMEOUT_MS` | 8000 | 单次 Critic HTTP 超时 |
| `QUALITY_GATE_MIN_CRITIC_RUNES` | 120 | 短于该自然语言长度的回答默认不送 Critic |
| `QUALITY_GATE_EXTRA_BODY_JSON` | 空 | 可选：合并进 Critic 请求体的 JSON 对象（如关闭思考模式） |
| `QUALITY_GATE_TOTAL_TIMEOUT_MS` | 25000 | crisp 进程总预算，必须小于宿主钩子 timeout |

**未配置 Critic 时，整个门禁只跑确定性规则并 fail-open（永不拦截）。** 生产使用必须配置，
否则你看到的永远是 PASS。宿主插件还各自有 `timeoutMs`、`mode`、`binaryPath`、`rewrite` 等配置，
见对应接入页。

### 3.3 数据边界

- 回答全文、以及可选的最近一条用户消息（≤ 2000 字符）会发送给配置的 Critic 服务；
  是否允许外发取决于所选服务与合规要求。
- 本地日志（crisp 为 stderr JSON、插件为 JSONL）只记录决策、规则类型、耗时、token 数等指标，
  **不记录回答全文、用户隐私或 API Key**。
- 插件把回答通过**本地 stdin** 交给 `crisp` 子进程；只有配置了 Critic 才会产生网络请求。
  详细边界见各接入文档的"数据边界"节与根 [README](../../README.md)。

### 3.4 一次重写（不可关闭的产品不变量）

每个 Turn/消息最多触发一次完整重写：阻塞型 Hook 依赖宿主提供的 `stop_hook_active`
（Codex/Claude 已验证），插件依赖**内存 guard**（DSH/opencode，进程重启会重置）。
任何接入实现都必须满足这一点，否则会出现重写循环与成本失控。

---

## 4. 共同验证命令

```powershell
# 1) 构建 + Go 测试（规则、Critic、决策、Hook 协议、check-file）
go build -o bin/crisp.exe ./cmd/crisp
go test ./...

# 2) 离线协议回放：不需要真实 CLI / 网络（12 个用例，覆盖设计 §12.4 + fail-open）
go run ./test/e2e/hookreplay -crisp bin/crisp.exe

# 3) 真实 CLI E2E
pwsh -File scripts/e2e-codex.ps1    # 需要 Codex CLI（默认路径或 -CodexPath；缺失会直接报错）
pwsh -File scripts/e2e-claude.ps1   # 自动发现 Claude Code CLI；缺失时 SKIPPED 且 exit 0

# 4) 插件单元测试（Node.js 20+，无需真实 CLI）
Push-Location integrations/dsh-plugin-crisp; node --test; Pop-Location
Push-Location integrations/opencode-crisp; node --test; Pop-Location

# 5) 文档相对链接自检（CI 同款）
go run ./test/docscheck
```

本机（2026-11-02，Windows / Go 1.26.4 / Node.js v22.19.0）实测结果：

| 检查 | 结果 |
| --- | --- |
| `go run ./test/e2e/hookreplay -crisp bin/crisp.exe` | 12/12 通过（约 611 ms） |
| `scripts/e2e-codex.ps1` | 8/8 场景通过（run-20261102-215036） |
| `scripts/e2e-claude.ps1` | 3/3 检查通过（run-20261102-221324） |
| `integrations/dsh-plugin-crisp` `node --test` | 57/57 通过 |
| `integrations/opencode-crisp` `node --test` | 51/51 通过 |

注意：Codex E2E 在找不到 CLI 时会直接失败（脚本要求 `-CodexPath` 或默认安装路径）；
Claude E2E 则会打印 `SKIPPED` 并退出 0，因此"Claude E2E 退出 0"本身不等于验证通过，
请检查输出是否为 `ALL 3 CHECKS PASSED`。

---

## 5. 已知边界（摘要）

| 接入 | 仍需注意 |
| --- | --- |
| Codex CLI | Windows 下 `hooks.json` 的 `timeout` 只丢弃超时输出、**不终止** Hook 进程（[codex-cli.md §7](codex-cli.md#7-timeout-field-and-measured-timeout-behavior)）；hook 需信任，项目层还需 project trust |
| Claude Code | 项目级 Hook 需要工作区受信任/审查（交互流程未点选验证）；用户级 `disableAllHooks: true` 会关闭所有 Hook；CLI 不在 PATH 时脚本会搜索 VS Code 扩展等位置（[claude-code.md §8](claude-code.md#8-verification-committed-reproducible)） |
| DeepSeek Harness | 无硬前置拦截，guard 是后置 turn；`plugin_manager install_bundle` 路径未实跑；仅 Windows 实测 |
| opencode | 回答已显示，无法拦截；`opencode run` 一次性模式可能丢检查（`serve`/TUI 可靠）；TUI toast 渲染未人工确认 |
| 通用协议 | 没有独立 E2E；新宿主必须按 checklist 自测后才算接入完成 |

各页的"未验证 / open items"是这一矩阵的权威来源，不要只根据摘要下结论。

---

## 6. 相关文档

- [设计与实施规格](../design.md)：目标、硬约束、Hook 协议、规则/Critic/决策设计
- [系统架构](../architecture.md)：组件与数据流、fail-open 矩阵、超时预算、扩展指南
- [排障手册](../troubleshooting.md)：Hook 不触发、总是 PASS、误拦截、超时、插件安装、Windows 问题
- [规则评测报告](../eval-report.md)：30 条样本规则评测、Hook 集成测试、真实 CLI E2E 汇总
- [文档中心](../README.md) / [根 README](../../README.md)
- 各接入文档：[Codex CLI](codex-cli.md) · [Claude Code](claude-code.md) · [DeepSeek Harness](deepseek-harness.md) · [opencode](opencode.md) · [通用协议](generic-hook-protocol.md)
