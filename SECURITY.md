# 安全策略

## 支持范围

| 版本 | 状态 |
| --- | --- |
| 0.1.x | 支持（当前开发线） |

## 报告漏洞

请**不要**通过公开 issue 报告安全问题。优先使用 GitHub 仓库的
**Security → Report a vulnerability**（私有漏洞报告）功能，并附上：

- 受影响版本 / commit
- 复现步骤与最小示例
- 影响判断（数据泄露、任意命令执行、拒绝服务、绕过质量门禁等）
- 是否已经在公开渠道暴露

我们会在确认后尽快回复并协调修复与披露时间。

## 威胁模型与设计边界

Crisp 在 Agent 回答交付前/后运行，会接触回答正文、可选的用户问题上下文和 Critic 配置：

1. **数据外发**：回答全文与最近一条用户消息（截断到 2000 字符）会发送给配置的
   `QUALITY_GATE_BASE_URL` 指向的 Critic 服务。请只配置你信任的服务；是否允许外发由部署方负责。
2. **API Key**：只从环境变量读取，仅用于 Critic 请求头；不写入仓库、日志或插件配置文件。
   日志脱敏规则在 [internal/logger/logger.go](internal/logger/logger.go)。
3. **本地进程边界**：Hook 模式由宿主启动本地 `crisp` 进程，通过 stdin/stdout 交换 JSON；
   插件模式通过子进程调用同一二进制。Crisp 不监听网络端口、不修改磁盘上的回答文件。
4. **Hook 信任**：Codex/Claude 只执行受信任的 Hook；自动化运行需要
   `--dangerously-bypass-hook-trust`（Codex）或等价信任配置。只在你审阅过 Hook 来源后使用。
5. **插件供应链**：`integrations/` 下的插件为仓库内私有包（未发布 registry）。从本仓库安装、
   审阅 `index.js` 与 `lib/` 后再启用；不要在不可信路径加载插件。
6. **拒绝服务**：Critic 超时、非法 JSON、网络错误都会 fail-open；Hook 侧有独立 timeout。
   如果 Critic 服务被恶意控制，最坏情况是错误判定，而不是删除回答内容——Crisp 只返回
   重写指令，不会修改文本。

## 加固建议

- 为 Critic 使用独立的低权限 API Key，并限制额度。
- 在 CI/自动化中不要把 Key 写到命令行参数（会出现在进程列表）；使用环境变量或密钥管理。
- 部署前运行 `go test ./...`、`go run ./test/docscheck` 与（如适用）插件单元测试。
- 定期审阅 `.codex/hooks.json`、`.claude/settings.json` 与插件配置的绝对路径是否指向预期二进制。
- 对高敏感仓库关闭回答外发（不配置 Critic），此时质量门禁只做本地规则检查并 fail-open。
