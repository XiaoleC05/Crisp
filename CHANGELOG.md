# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 风格，
版本号遵循 [Semantic Versioning](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### Added

- DSH Hint 插件新增 **`critic: harness`** 模式：语义审查改走 Harness 自身的 `ctx.llm`
  （可使用已登录的 DeepSeek-V4.1-Flash，`reasoningEffort: off` 关闭思考模式），无需外部
  OpenAI-compatible endpoint 或 API Key；确定性规则仍由本地 `crisp` 二进制完成。
  外部 HTTP Critic 仍为默认模式。observe 模式下审查异步执行、不增加对话延迟；guard 模式在
  turn 结束前等待判定以排队一次重写。见 [docs/integrations/deepseek-harness.md](docs/integrations/deepseek-harness.md)。
- Critic 新增 ~~QUALITY_GATE_EXTRA_BODY_JSON~~：把 provider 专有字段合并进请求体，例如用
  ~~{"thinking":{"type":"disabled"}}~~ 关闭 DeepSeek-V4.1-Flash 的思考模式。已用真实官方 API
  验证：重复结论样本返回 block（semantic_duplicate，约 1.4s），正常技术样本返回 pass（约 0.6s）。
- Critic 默认评审偏好改为**结论先行、整体结构化**：铺垫实质性延迟/遮蔽结论或把同一观点拆碎
  才算 material；短回答不因缺少标题被拦截，长而信息密度高的回答不扣分。固定重写指令同步加入
  "先在第一段给出直接结论，再按清晰结构展开"（Go policy、DSH/opencode 插件三处逐字一致）。

## [0.1.0] - 2026-11-02

首个可用版本：Go 质量检查核心 + 多 Agent 接入 + 回归测试与真实 CLI E2E。

### Added

- **检查核心**：确定性规则（重复段落/句子、无效开场白/结束语、结构异常、字数风险信号）、
  OpenAI-compatible LLM Critic、统一 PASS/BLOCK/FAIL-OPEN 决策、固定可执行重写指令；
  每 Turn 最多一次重写；stdout 单 JSON、stderr 结构化日志与脱敏。
- **Codex CLI Stop Hook**：`.codex/hooks.json` 接入、安装脚本、stop_hook_active 防循环、
  trust gate 与 timeout 语义的实测记录（[docs/integrations/codex-cli.md](docs/integrations/codex-cli.md)）。
- **Claude Code Stop Hook**：共享 Stop 协议兼容、`.claude/settings.json`、安装脚本、
  Claude transcript 工具结果过滤、真实 Claude Code 2.1.312 E2E
  （[docs/integrations/claude-code.md](docs/integrations/claude-code.md)）。
- **`crisp check-file`**：对 Markdown/文本文件运行同一检查流水线，支持多文件、`--json`、stdin、
  明确退出码与读取失败 fail-open。
- **DeepSeek Harness Cordis 插件**：`llm/stream` 观察、observe/guard 模式、结构化 JSONL、
  可选的 agent.followup 后置一次重写；隔离 DSH_HOME 的 headless E2E
  （[docs/integrations/deepseek-harness.md](docs/integrations/deepseek-harness.md)）。
- **opencode 插件**：session 完成事件触发检查、toast、一次重写 guard、JSONL/app.log 日志
  （[docs/integrations/opencode.md](docs/integrations/opencode.md)）。
- **回归与评测**：30 条中文为主（含英文技术术语）的样本（20 问题 / 10 正常）、规则层覆盖率/误杀率评测、
  Hook 集成测试表、Codex 8/8 与 Claude 3/3 真实 CLI E2E、插件单元测试 57 + 51。
- **文档体系**：README、设计规格、架构说明、接入指南、E2E 记录、评测报告、排障手册、
  文档索引与通用 Hook 协议。

### Changed

- 接入文档统一到 `docs/integrations/`：原 `docs/codex-e2e.md` →
  `docs/integrations/codex-cli.md`，原 `docs/dsh-integration.md` →
  `docs/integrations/deepseek-harness.md`。

### Known limitations

- 真实 Critic 模型一致性与重写改善率尚未用真实服务测量；当前 E2E 使用本地 mock 验证协议与循环。
- DSH / opencode 没有 Stop block 协议，只能 observe + 后置一次重写（DSH 默认 observe）。
- Codex CLI 在 Windows 上 timeout 只丢弃超时输出、不终止 Hook 进程（Crisp 自身 25s 预算负责退出）。
- 未配置 Critic 时仅规则检查并 fail-open；插件包未发布到 npm/pnpm registry。
- 对不支持扩展的 Agent 的本地 API Proxy 尚未实现。
