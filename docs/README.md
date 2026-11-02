# Crisp 文档

这里是 Crisp 的文档中心。Crisp 是一个面向终端 Coding Agent 的输出质量门禁：
确定性规则 + LLM Critic，默认放行，只有明确实质问题才要求完整重写，任何内部异常 fail-open。

## 按目标进入

| 我想…… | 去看 |
| --- | --- |
| 先跑起来、接入我的 Agent | [仓库 README](../README.md) |
| 理解需求、协议与实施规格 | [设计与实施规格](design.md) |
| 理解内部实现、失败语义与扩展方式 | [架构说明](architecture.md) |
| 评估规则/Critic 效果与验证证据 | [评测报告](eval-report.md) |
| 遇到 Hook 不触发、误拦截、超时等问题 | [排障手册](troubleshooting.md) |
| 接入一个新的 Agent | [接入文档索引](integrations/README.md) |
| 了解安全边界与漏洞报告方式 | [SECURITY.md](../SECURITY.md) |

## 文档地图

| 文档 | 内容 |
| --- | --- |
| [design.md](design.md) | 第一版实施规格：目标、硬约束、Hook 协议、规则/Critic/决策设计、测试体系 |
| [architecture.md](architecture.md) | 组件与数据流、fail-open 矩阵、超时预算、可观测性、安全边界、扩展指南、设计取舍 |
| [eval-report.md](eval-report.md) | 30 条样本的规则层评测、Hook 集成测试、真实 CLI E2E 汇总、复现命令与局限 |
| [troubleshooting.md](troubleshooting.md) | 按症状排查：不触发、总 PASS、误拦截、超时、插件安装、Windows 特有问题 |
| [integrations/README.md](integrations/README.md) | 各 Agent 接入方式的选择矩阵与共享前置条件 |
| [integrations/codex-cli.md](integrations/codex-cli.md) | Codex CLI Stop Hook：协议、trust、timeout 实测、8/8 E2E |
| [integrations/claude-code.md](integrations/claude-code.md) | Claude Code Stop Hook：协议兼容、transcript 处理、3/3 E2E |
| [integrations/deepseek-harness.md](integrations/deepseek-harness.md) | DeepSeek Harness Cordis 插件：llm/stream、observe/guard、headless guard E2E |
| [integrations/opencode.md](integrations/opencode.md) | opencode 插件：事件钩子、toast、一次重写 guard、serve E2E |
| [integrations/generic-hook-protocol.md](integrations/generic-hook-protocol.md) | 通用 stdin/stdout Hook 契约：适用于其他终端 Agent |

## 文档语言

README、设计、架构、评测、排障与 opencode/通用协议接入页使用中文；Codex CLI、Claude Code、
DeepSeek Harness 三篇接入页保留英文原文，因为其中包含直接整理自验证脚本的协议与实测细节。
新增文档默认使用中文；同一事实冲突时以中文文档与其引用的代码/脚本为准。

## 文档维护约定

- 新增/移动文档后运行 `go run ./test/docscheck`，CI 会检查所有相对链接。
- 接入文档统一放在 [integrations/](integrations/)，每篇必须列出"已验证 / 未验证"。
- 行为或数字变化时同步更新 README、受影响的 docs 与 [CHANGELOG.md](../CHANGELOG.md)。
- 实测结论必须注明宿主版本、命令与证据位置；未实测的内容不要写成已验证。
