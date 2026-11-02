# Crisp 评测报告

本报告汇总 Crisp v0.1.0 的四层验证：**规则层评测**、**Hook / 插件集成测试**、**真实 CLI 端到端验证**
与 **Critic 协议验证**，并说明目前**尚未测量**的真实模型质量指标、复现方式与结论的适用边界。

- 评测日期：2026-11-02
- 代码：`main` 分支（规则层数据采集于 2026-11-02 的 main；其余验证在后续 main 提交上执行）
- 环境：Windows x64 / Go 1.26.4 / Node.js v22.19.0
- 样本：[testdata/cases/](../testdata/cases/)（20 条问题回答 + 10 条正常回答，共 30 条）
- 模型与 Critic 调用均为本地 mock（宿主安装/依赖步骤可能联网）；不使用用户凭据、不修改用户配置。

## 1. 环境与宿主版本

| 组件 | 版本 | 说明 |
| --- | --- | --- |
| Go | 1.26.4 windows/amd64 | 核心检查器（零第三方依赖） |
| Node.js | v22.19.0 | 插件单测（`node:test`） |
| Codex CLI | 0.145.0-alpha.18 | 真实 Stop Hook E2E |
| Claude Code | 2.1.312 | 真实 Stop Hook E2E（VS Code 扩展内置 native binary） |
| opencode | 1.18.18 | 真实插件 E2E（`serve` 模式） |
| DeepSeek Harness | 0.2.1-rc.5 | 隔离 scratch profile 下的 Host 插件验证 |
| Critic 服务 | 本地 hermetic mock | 无真实 provider 凭据；协议与失败路径已验证，模型质量未测 |

## 2. 评测方法

| 层 | 目标 | 方法 | 证据 |
| --- | --- | --- | --- |
| L1 规则层 | 规则覆盖/误杀/延迟 | 30 条样本各跑一次 `checker.Analyze` | `go test ./cmd/crisp -run TestRuleEvaluation -v` |
| L2 集成层 | Hook 协议、fail-open、check-file | Go 表驱动测试（124 个测试函数 / 15 个测试文件） | `go test ./...` |
| L3 真实宿主 | 真实 CLI 的拦截/重写/防循环 | 本地 mock 模型 + mock Critic + 真实 CLI | `scripts/e2e-codex.ps1`、`scripts/e2e-claude.ps1`、DSH/opencode 文档 |
| L4 Critic 协议 | 请求/响应 schema、失败语义 | httptest hermetic mock | `go test ./internal/critic -cover` |

L3 的每个场景都由脚本断言：模型请求次数、Critic 请求次数、`stop_hook_active` 变化、
最终输出是否为重写后的回答，并保存 `summary.json` 与日志。

## 3. 规则层结果（30 条样本）

### 3.1 总表

| 类别 | 样本数 | 规则发现数 | 有信号 | material | 送 Critic |
| --- | --- | --- | --- | --- | --- |
| duplicate_conclusion | 5 | 5 | 5 | 5 | 5 |
| filler_opening | 5 | 10 | 5 | 5 | 5 |
| over_fragmentation | 5 | 5 | 5 | 0 | 5 |
| redundant_explanation | 5 | 0 | 0 | 0 | 5 |
| 问题样本合计 | 20 | 20 | 15 | 10 | 20 |
| good_long | 5 | 0 | 0 | 0 | 5 |
| good_short | 5 | 0 | 0 | 0 | 0 |
| 正常样本合计 | 10 | 0 | 0 | 0 | 5 |

### 3.2 核心指标

| 指标 | 结果 | 说明 |
| --- | --- | --- |
| 规则信号覆盖率（全部问题样本） | 15/20 = 75% | 剩余 5 条是纯语义类别 redundant_explanation，按设计不由规则覆盖 |
| 规则信号覆盖率（规则目标类别） | 15/15 = 100% | duplicate_conclusion / filler_opening / over_fragmentation 各 5/5 |
| 规则 material 命中率 | 10/20 = 50% | duplicate_conclusion 5/5、filler_opening 5/5；structure 为 minor，语义类别无信号 |
| 规则误杀率（正常样本任意发现） | 0/10 = 0% | 10 条正常样本零规则发现，balanced 下正常长篇才会送 Critic |
| 规则误杀率（正常样本 material） | 0/10 = 0% | — |
| 送 Critic 比例（问题样本） | 20/20 = 100% | balanced 模式下全部问题样本都会得到语义审查 |
| 规则平均耗时 | 207.6 µs/条 | 本机单次记录；同进程 30 条样本，最大 551.5 µs（随硬件/负载变化） |

**结论**：规则层负责"可机械识别"的重复与填充语，零误杀；过度拆分/无效解释这类纯语义问题
按设计交给 Critic。因此生产默认使用 balanced 模式；economy 只送有规则发现的样本，会漏检纯语义类别。

## 4. Hook / CLI 集成测试（L2）

Go 测试共 **124 个测试函数 / 15 个测试文件**，`go test ./...` 全绿；协议 replay 12/12。

### 4.1 Hook 协议与决策表

| 场景 | 预期 | 结果 |
| --- | --- | --- |
| 正常回答 | `{}` | PASS |
| 第一次发现 material 问题 | block + 固定重写指令 | PASS |
| 重写后的第二次 Stop（`stop_hook_active=true`） | `{}` | PASS |
| `stop_hook_active` 缺失 | 放行并告警 | PASS |
| Hook 输入解析失败 | 放行并记录异常 | PASS |
| `last_assistant_message` 缺失/为空 | 放行 | PASS |
| 短回答（< 120 natural runes 且无规则发现） | 不调用 Critic，放行 | PASS |
| Critic 超时 | 放行，`fail_open=true` | PASS |
| Critic 返回非法 JSON / 未知枚举 | 放行，`fail_open=true` | PASS |
| Critic HTTP 500 / 未配置 | 放行，`fail_open=true` | PASS |
| 回答全文 / API Key 写入 stderr | 不应出现 | PASS（日志脱敏测试） |
| 任意场景 stdout | 永远一个合法 JSON | PASS |
| Codex / Claude 形态 payload 兼容 | 解析一致 | PASS（internal/hook 测试） |

### 4.2 协议 replay（test/e2e/hookreplay）

对真实二进制重放 12 个 hook 场景（pass/block/minor/loop guard/字段缺失/非法输入/Critic 故障/
短回答），全部 PASS：

```powershell
go run ./test/e2e/hookreplay -crisp bin/crisp.exe
# 12/12 replay cases passed
```

### 4.3 check-file

`crisp check-file` 覆盖单文件/多文件/`--json`/stdin/读取失败/Critic 故障/短回答跳过，
退出码语义（0 通过、1 BLOCK、2 错误）由 Go 测试断言。

## 5. 真实 CLI 端到端验证（L3）

### 5.1 Codex CLI 0.145.0-alpha.18 — 8/8 通过

命令：`pwsh -File scripts/e2e-codex.ps1`（隔离 `CODEX_HOME` + scratch 项目 + mock Responses provider）。

| 场景 | 断言结果 |
| --- | --- |
| S0 协议 replay | 12/12 |
| S1 正常放行 | hook 触发一次；decision=pass；critic_called=true；回答交付一次 |
| S2 block → 重写 → 二次 Stop | 恰好 1 次重写；`stop_hook_active` false→true；模型请求 2 次、Critic 1 次 |
| S3 Hook trust gate | 无 `--dangerously-bypass-hook-trust` 时 Hook 不执行 |
| S4a 超时（timeout=2 + 4s hook） | Stop Failed，迟到的 block 被忽略（不重写） |
| S4b 超时内（timeout=30 + 1.5s hook） | Stop Completed，恰好 1 次重写 |
| S5 checked-in `.codex/hooks.json` | 真实 CLI 调用成功 |
| S6 项目 trust gate | 未受信任时项目级 Hook 不加载 |

详见 [docs/integrations/codex-cli.md](integrations/codex-cli.md)。已知 Windows 差异：
timeout 只丢弃 Hook 输出、不终止 Hook 进程。

### 5.2 Claude Code 2.1.312 — 3/3 通过

命令：`pwsh -File scripts/e2e-claude.ps1`（隔离 `CLAUDE_CONFIG_DIR` + mock Anthropic Messages API）。

| 场景 | 断言结果 |
| --- | --- |
| S1 block → 重写 → 二次 Stop | 真实 payload；`stop_hook_active` false→true；模型请求 2 次、Critic 1 次 |
| S2 checked-in `.claude/settings.json` 直连 | 真实 CLI 调用成功，1 次重写 |
| isolation | 真实 `~/.claude` SHA256 前后一致 |

详见 [docs/integrations/claude-code.md](integrations/claude-code.md)。

### 5.3 DeepSeek Harness 0.2.1-rc.5 — Host 插件

- 隔离 scratch profile：`dsh plugin add` 注册 bundle、`--dump-config` 显示插件行、真实 boot 激活。
- 真实 `dsh headless` guard E2E：缺陷回答 block → 恰好 1 次 `agent.followup` → 重写 turn →
  pass；durable session log 中恰好 1 条重写指令；session-title/compaction 请求不被误检。
- 插件单测 57/57。
- 复现：`pwsh -File scripts/e2e-dsh.ps1`（需要本机安装 DeepSeek Harness；缺失时 SKIPPED）。

详见 [docs/integrations/deepseek-harness.md](integrations/deepseek-harness.md)。

### 5.4 opencode 1.18.18 — 插件

- 真实 `opencode serve` + 本地 mock OpenAI-compatible provider：pass 决策写入 JSONL/app.log；
  block 后触发一次 `promptAsync` 重写，重写结果被 guard 跳过。
- 插件单测 51/51。
- 复现步骤见 [docs/integrations/opencode.md](integrations/opencode.md)（含 e2e/ 下的 mock 与 drive 脚本）。

## 6. 插件单元测试（L4 补充）

| 插件 | 测试数 | 覆盖重点 |
| --- | --- | --- |
| `integrations/dsh-plugin-crisp` | 57 | 配置默认值/校验、流式累积、crisp 子进程调用、超时/非法 JSON/非零退出 fail-open、turn 跟踪、guard 一次重写、日志脱敏 |
| `integrations/opencode-crisp` | 51 | 事件提取、配置优先级、crisp 调用、rewrite guard、失败路径、日志、真实 `bin/crisp.exe` 子进程（含 hermetic） |

两者均为 `node:test`、无 npm 依赖、无网络。

## 7. FAIL-OPEN 与错误路径

| 故障 | 期望 | 验证方式 |
| --- | --- | --- |
| Hook 输入为空/非法/超大/尾部多余数据 | 放行 | internal/hook + cmd/crisp 测试 |
| `stop_hook_active` 缺失 | 放行并告警 | cmd/crisp 测试 |
| Critic 超时 / 非 2xx / 非法 JSON / 未知枚举 | 放行，`fail_open=true` | cmd/crisp + critic 测试 |
| Critic 未配置 | 放行，日志 `critic_error` | cmd/crisp 测试 |
| 规则分析 panic | 内部 recover + 空报告 | checker 防御性 recover（纯函数，回归测试覆盖异常输入） |
| 插件子进程 spawn/超时/坏 JSON | 放行 | 两个插件的 node:test |
| DSH provider transport 失败 | 插件记录 skip 事件，不阻塞 | 真实 dsh headless 失败 provider 验证 |

结论：在所有已测故障路径中，没有一次"本应放行却被拦截"。

## 8. 延迟与成本

| 指标 | 实测/说明 |
| --- | --- |
| 规则层分析 | 平均 207.6 µs/条，最大 551.5 µs（30 条样本） |
| Hook 进程自身（mock Critic 本地） | 约 9–30 ms/次（Codex E2E 记录 [18, 23, 30] ms 等样本） |
| 插件决策（mock Critic 本地） | 约 20 ms（opencode E2E 一次 21 ms；DSH 同量级） |
| Critic 真实延迟 | **未测**；由 provider 网络与模型决定，受 `QUALITY_GATE_CRITIC_TIMEOUT_MS`（8s）约束 |
| Critic token 用量 | Verdict.Usage 记录 prompt/completion/total（示例 120 tokens） |
| 总 Token 成本 | `T_total = T_original + T_critic + T_rewrite`；宿主侧 original/rewrite 需从宿主会话用量取得，不能只用 Hook 的 critic_tokens |

## 9. 尚未测量的指标（需要真实 Critic / 真实模型）

1. **Critic 判定一致性**：30 条样本 × 3 次取多数，统计与人工标注的一致率。需要配置真实
   `QUALITY_GATE_BASE_URL/API_KEY/MODEL` 后运行。
2. **重写改善率**：真实 Codex/Claude 会话中触发 block 的回答，收集原回答与重写回答，
   人工抽检（缺陷是否消除、事实/数字/代码是否保留）。改善率 = 重写后无 material 缺陷的比例。
3. **真实 Critic 延迟与成本**：需要 provider 侧计量。

建议命令（配置真实 Critic 后）：

```powershell
# 对每条样本重复 3 次，记录 decision/severity/issue type，取多数后人工抽检
Get-Content testdata/cases/positive.jsonl | ForEach-Object {
  ($_ | ConvertFrom-Json).answer | .\bin\crisp.exe check-text
}
# 规则层 + Hook 集成回归
go test ./cmd/crisp -run TestRuleEvaluation -v
go test ./...
```

## 10. 复现命令汇总

| 目标 | 命令 | 前置条件 |
| --- | --- | --- |
| Go 单测 + 集成测试 | `go test ./...` | Go 1.26+ |
| 规则层评测 | `go test ./cmd/crisp -run TestRuleEvaluation -v` | Go 1.26+ |
| Hook 协议 replay | `go run ./test/e2e/hookreplay -crisp bin/crisp.exe` | 先构建 bin/crisp.exe |
| Codex 真实 E2E | `pwsh -File scripts/e2e-codex.ps1` | Codex CLI；缺失时自动 SKIPPED |
| Claude 真实 E2E | `pwsh -File scripts/e2e-claude.ps1` | Claude Code CLI；缺失时 SKIPPED |
| DSH guard E2E | `pwsh -File scripts/e2e-dsh.ps1` | 安装 DeepSeek Harness；缺失时 SKIPPED |
| 插件单测 | `node --test`（两个 integrations 目录） | Node.js 20+ |
| 文档链接 | `go run ./test/docscheck` | Go 1.26+ |

## 11. 结论与适用边界

**结论**

- 规则层在 30 条中英文样本上零误杀，规则目标类别全部命中；语义类别按设计交给 Critic。
- 两种阻塞型 Stop Hook（Codex/Claude）在真实 CLI 上验证了"block → 完整重写 → 二次 Stop 放行"
  的完整链路，每个 Turn 恰好一次重写。
- 两种插件宿主（DSH/opencode）验证了 observe/guard 语义与一次重写 guard；它们没有交付前 block
  协议，属于后置修正。
- 所有已测异常路径 fail-open，没有"本应放行却被拦截"的案例。

**边界与风险**

- L3 使用 mock 模型与 mock Critic：验证的是**协议、循环与决策链路**，不是**模型判定质量**。
- 样本量 30 条、单机 Windows、以中文为主；换模型/语言/宿主版本后需复测阈值与 prompt。
- 插件宿主的后置重写会多一轮可见对话与模型调用；DSH 默认 observe，需要显式开启 guard。
- 真实 Critic 一致性、重写改善率与总 Token 成本仍需真实服务与真实会话测量（见第 9 节）。
