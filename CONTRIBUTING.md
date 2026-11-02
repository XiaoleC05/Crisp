# 贡献指南

感谢参与 Crisp。Crisp 是一个 Agent 输出质量门禁：确定性规则 + LLM Critic + 统一决策，
默认放行、只有明确实质问题才拦截、每个 Turn 最多一次重写、任何内部异常 fail-open。

## 开发环境

- Go 1.26+（核心检查器只使用标准库，不引入第三方依赖）
- Node.js 20+（仅插件接入层使用内置 `node:test`，无 npm 依赖）
- PowerShell 7+（Windows 脚本；Linux/macOS 可只跑 Go 部分）
- 可选：Codex CLI、Claude Code CLI（仅真实 E2E 需要；缺失时脚本会 SKIPPED）

## 快速开始

```powershell
go build -o bin/crisp.exe ./cmd/crisp
go vet ./...
go test ./...
```

插件单元测试：

```powershell
Push-Location integrations/dsh-plugin-crisp; node --test; Pop-Location
Push-Location integrations/opencode-crisp; node --test; Pop-Location
```

文档链接检查：

```powershell
go run ./test/docscheck
```

## 仓库结构

| 路径 | 说明 |
| --- | --- |
| `cmd/crisp/` | 进程入口：Stop Hook、`check-text`、`check-file` |
| `internal/hook/` | Codex / Claude Code 共享 Stop Hook 协议解析与输出 |
| `internal/checker/` | 确定性规则（重复、填充语、结构、长度信号） |
| `internal/critic/` | OpenAI-compatible Critic 客户端、prompt、严格 verdict 解析 |
| `internal/policy/` | 是否需要 Critic、最终决策、固定重写指令 |
| `internal/logger/` | stderr 结构化 JSON 日志与脱敏 |
| `integrations/` | DSH（Cordis）/ opencode 插件 |
| `testdata/cases/` | 30 条回归样本（20 问题 / 10 正常） |
| `test/e2e/` | 真实 CLI E2E 驱动与 mock 服务 |
| `docs/` | 设计、架构、接入、E2E、评测、排障文档 |

## 核心不变量（修改代码时必须保持）

1. **默认放行**：只有 Critic 返回 `decision=block` 且 `severity=material` 才产生 BLOCK；
   规则发现只作为 Critic 的输入，不单独拦截。
2. **FAIL-OPEN**：解析失败、超时、非 2xx、非法 JSON、未配置 Critic、panic 都不能拦截回答。
3. **每 Turn 最多一次重写**：阻塞型 Hook 依赖 `stop_hook_active`；插件依赖内存 guard。
4. **stdout 只输出一个合法 JSON 对象**，所有日志写 stderr（插件为 JSONL）。
5. **不记录完整回答、用户隐私、API Key**；日志只记录决策、规则类型、耗时、token 数等指标。
6. **不通过正则删除内容**：只生成可执行的重写指令，让 Agent 重新生成完整回答。
7. **Go 模块零第三方依赖**；插件不引入 npm 依赖。
8. 重写指令必须保留全部必要事实、数字、代码和解释，不能为精简而删信息。

## 修改与测试

- 规则阈值集中在 [internal/checker/checker.go](internal/checker/checker.go) 顶部；修改后必须跑
  `go test ./cmd/crisp -run TestRuleEvaluation -v` 并更新 [docs/eval-report.md](docs/eval-report.md)。
- 新增接入：先阅读 [docs/integrations/generic-hook-protocol.md](docs/integrations/generic-hook-protocol.md)
  与 [docs/architecture.md](docs/architecture.md) 的扩展指南；接入文档放在 `docs/integrations/`。
- Hook 协议测试：`go test ./internal/hook`；Hook 集成表：`go test ./cmd/crisp`。
- 真实 CLI E2E（需要对应 CLI，缺失时自动 SKIPPED）：

```powershell
pwsh -File scripts/e2e-codex.ps1
pwsh -File scripts/e2e-claude.ps1
```

- 行为变更必须同步更新：README、相关 docs、CHANGELOG.md；新增或移动文档后运行
  `go run ./test/docscheck`。

## 提交规范

- 使用 Conventional Commits 前缀：`feat`、`fix`、`test`、`docs`、`chore`、`refactor`；
  例如 `feat(checker): add paragraph similarity rule`。
- 一个提交聚焦一个模块；提交信息说明"为什么"而不是流水账。
- 提交前确保 `go vet ./...`、`go test ./...`、插件 `node --test`、`go run ./test/docscheck` 全绿。

## Pull Request 检查清单

- [ ] 核心不变量未被破坏（默认放行 / fail-open / 一次重写 / 日志脱敏）
- [ ] 新增或修改行为有对应测试（Go 单测、Hook 集成测试或插件 node:test）
- [ ] 文档与 CHANGELOG 已同步，`go run ./test/docscheck` 通过
- [ ] 没有提交 API Key、用户数据、真实会话内容或 `.tmp/` 产物
- [ ] 接入行为差异（交付前拦截 vs 后置重写）在文档中如实描述

## 安全与问题反馈

- 安全漏洞请按 [SECURITY.md](SECURITY.md) 的方式私下报告，不要公开 issue。
- 普通 bug / 功能建议请附带复现命令、版本（`crisp version`、Go/Node 版本、宿主版本）
  与相关日志（注意先去除隐私信息）。
