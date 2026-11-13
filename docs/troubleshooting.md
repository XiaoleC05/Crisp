# Crisp 故障排查指南

本指南按"症状 → 排查步骤 → 修复"组织，覆盖 Codex CLI、Claude Code、DeepSeek Harness（DSH）、
opencode、`crisp check-file` 与 Windows 环境。命令分两类：crisp / Go / Node 相关命令已于 2026-11-02 在本仓库（Windows）实际执行或验证参数；宿主相关命令（codex / claude / dsh / opencode）以仓库验证文档与脚本参数为准，未在此环境逐一实际执行，按"视安装而定 / 视版本而定"判断。
API Key 一律不要粘贴进日志或工单。

## 目录

1. [快速分诊](#1-快速分诊)
2. [通用诊断命令](#2-通用诊断命令)
3. [Hook 完全不触发](#3-hook-完全不触发)
4. [回答总是 PASS](#4-回答总是-pass)
5. [误拦截 / 过严](#5-误拦截--过严)
6. [慢 / Critic 超时](#6-慢--critic-超时)
7. [check-file 问题](#7-check-file-问题)
8. [DSH 插件问题](#8-dsh-插件问题)
9. [opencode 插件问题](#9-opencode-插件问题)
10. [Windows 专有问题](#10-windows-专有问题)
11. [诊断信息收集清单](#11-诊断信息收集清单)

## 1. 快速分诊

| 现象 | 最可能原因 | 章节 |
| --- | --- | --- |
| Stop Hook 完全没反应 | Hook 未受信任 / 配置路径不对 / 字段名写错 / 项目未受信任 | 第 3 节 |
| 回答总是 PASS | Critic 未配置（fail-open）/短回答跳过 / economy 模式 / 插件 observe 模式 | 第 4 节 |
| 误拦截或过严 | 规则信号把回答送 Critic；Critic rubric/模型偏严 | 第 5 节 |
| 审查很慢 | Critic 网络/超时、预算配置不合理 | 第 6 节 |
| `check-file` 退出码非预期 | 用法错误、文件超 4 MiB、flags 位置 | 第 7 节 |
| DSH 插件没有日志 | 未安装/未重启/日志写 stderr 不可见/binary 路径错误 | 第 8 节 |
| opencode 插件没有日志 | `--pure`、导出不合规、`run` 模式、插件未登记 | 第 9 节 |
| Windows 超时/引号/BOM 问题 | Codex timeout 不杀进程、shell 选择、BOM 破坏 JSON | 第 10 节 |

## 2. 通用诊断命令

### 2.1 二进制与构建

```powershell
.\bin\crisp.exe version                    # 期望输出 crisp 0.1.0
.\bin\crisp.exe --help                     # 子命令与环境变量说明
go version                                  # 期望 go1.26.x（go.mod 要求 go 1.26）
gofmt -l .                                  # 无输出表示格式正确
go vet ./...                                # 无输出且退出码 0
```

若 `bin\crisp.exe` 不存在：`pwsh -File scripts/build.ps1`（重建会覆盖 `bin\crisp.exe`）；或直接 `go build -o bin/crisp.exe ./cmd/crisp`（见 README）。

### 2.2 测试与协议回放

```powershell
go test ./...                                                   # 全部包通过
go test ./cmd/crisp -run TestRuleEvaluation -count=1 -v         # 30 条样本规则评测
go run ./test/e2e/hookreplay -crisp bin/crisp.exe               # 12/12 协议回放
Push-Location integrations/dsh-plugin-crisp; node --test; Pop-Location   # 57 tests
Push-Location integrations/opencode-crisp;  node --test; Pop-Location   # 51 tests
```

本次复跑结果：Go 测试全部通过、hookreplay 12/12、DSH 111/111、opencode 51/51。真实的 Codex / Claude Code E2E 需要对应 CLI；脚本在 CLI 缺失时打印 `SKIPPED` 并以 0 退出：

```powershell
pwsh -File scripts/e2e-codex.ps1            # -? 查看参数：-CodexPath -SkipBuild -SkipReplay -ScenarioTimeoutSec -WorkRoot
pwsh -File scripts/e2e-claude.ps1           # -? 查看参数：-ClaudePath -SkipBuild -ScenarioTimeoutSec -WorkRoot
```

### 2.3 手工模拟一次 Stop Hook

```powershell
'{"session_id":"s","stop_hook_active":false,"last_assistant_message":"这是一段足够长的正常回答。"}' | .\bin\crisp.exe
# 未配置 Critic 时输出 {}（短回答不会调用 Critic）
```

## 3. Hook 完全不触发

### 3.1 先排除共用原因

1. `Test-Path .\bin\crisp.exe`：二进制不存在则 Hook 命令必然失败；先构建。
2. 配置里的 `command` 是绝对路径且真实存在（Codex 直接按可执行文件路径执行，不经过 shell；Claude 用 `& '<路径>'` 且 `shell` 为 `powershell`）。
3. 手工跑一次 2.3 的命令：能输出 `{}` 说明二进制本身没问题，问题在宿主配置或信任。
4. Hook 必须是**阻塞**的：后台/异步 Hook（`async: true`）无法在 turn 结束前返回决策，Stop Hook 不会生效。

### 3.2 Codex CLI

1. 版本与 hooks 功能：`codex --version`；该版本 `codex features list` 中 `hooks` 为 `stable true`（默认启用）。
2. 配置文件位置：项目级 `<repo>/.codex/hooks.json`，用户级 `$CODEX_HOME/hooks.json`。用户级始终读取；项目级只在项目受信任时加载（见第 4 步）。
3. JSON 形状是否正确：`hooks.Stop[]` → `hooks[]`；字段 `type: "command"`、`command`、`timeout`（秒）。注意 `timeout_sec`（Rust 内部字段）与 `timeoutSec`（App Server 字段）**不是** hooks.json 字段，写错时超时可能被静默忽略（具体回退行为视 Codex 版本而定，本仓库未单独测量）。
4. 项目信任：项目级 Hook 只在项目被信任时加载，用户配置 `$CODEX_HOME/config.toml` 中需要有 `[projects.'<项目绝对路径>'] trust_level = "trusted"`。
5. Hook 信任：只有被审查/信任的 Hook 才会执行。交互式 TUI 首次使用会提示审查；自动化运行用 `codex exec --dangerously-bypass-hook-trust`（仅限已确认 Hook 来源）。
6. 确认 Hook 是否被调用：`codex exec ... 2>&1 | Select-String 'hook:'`，应能看到 `hook: Stop`、`hook: Stop Completed` / `hook: Stop Blocked` / `hook: Stop Failed`。完全看不到 `hook:` 说明 Hook 未被加载或信任。
7. Codex 的 `codex exec` 输出不会显示 Crisp 的 stderr JSON。需要看日志时，用测试包装器 `test/e2e/hooktap`（`go build -o .tmp\bin\hooktap.exe ./test/e2e/hooktap`，参数 `-crisp`、`-out`）临时包裹；生产配置应直连 `crisp.exe`。
8. 重新安装项目配置：`pwsh -File scripts/install-codex-hook.ps1 -Force`（`-?` 可查看 `-CrispBinary`、`-TimeoutSeconds`、`-HooksPath`、`-Force`）。

### 3.3 Claude Code

1. `claude --version`（本机可能不在 PATH；也可是 VS Code/Cursor 扩展内置的 `native-binary/claude.exe`）。
2. 配置位置：项目级 `.claude/settings.json`、用户级 `$CLAUDE_CONFIG_DIR/settings.json`（默认 `~/.claude/settings.json`），结构为 `hooks.Stop[]` → `matcher` + `hooks[]`。
3. 是否被禁用：任一层设置 `disableAllHooks: true` 都会禁用全部 Hook。本仓库开发机的用户级 `~/.claude/settings.json` 当前就是 `true`；用隔离配置或先改回 `false`。另外 `--bare` 会跳过 settings Hook，托管/企业策略也可能禁用它（视环境而定）。
4. 权限与信任：用户级 Hook 可直接运行；项目/本地 settings Hook 需要受信任工作区，并可能要求 Hook 审查确认（交互式流程）。
5. 阻塞性：不要给 Stop handler 加 `async: true`，否则 block 决策无法在 turn 结束前送达。
6. Windows shell：Claude Code 在装有 Git Bash 时默认用 bash，Windows 路径会被错误解析；handler 需要 `"shell": "powershell"`（仓库 `.claude/settings.json` 与安装脚本已内置）。
7. 重新安装：`pwsh -File scripts/install-claude-hook.ps1 -Force`；安装脚本会合并而不是覆盖其他 settings。
8. 真实 E2E 复现：`pwsh -File scripts/e2e-claude.ps1`（CLI 缺失时 SKIPPED + 退出码 0；用 `-ClaudePath` 指定路径）。

## 4. 回答总是 PASS

先分清"门禁放过"和"门禁根本没生效"：

1. 手工确认二进制：`'{"session_id":"s","stop_hook_active":false,"last_assistant_message":"<一段较长回答>"}' | .\bin\crisp.exe` 并观察 stderr（Hook 模式下 stderr 可能被宿主吞掉，可在本地手工跑或用第 3 节的包装器）。
2. 看 stderr 里的 `quality_check`：`critic_called`、`fail_open`、`reason_type` 能直接回答"为什么 PASS"。

常见原因与判定方法：

| 原因 | stderr 证据 | 修复 |
| --- | --- | --- |
| Critic 未配置（整体 fail-open） | `critic_unavailable`，`quality_check fail_open=true reason_type=critic_error` | 设置 `QUALITY_GATE_BASE_URL` + `QUALITY_GATE_MODEL`（本地服务可不要 Key） |
| 短回答跳过 Critic | `quality_check critic_called=false reason_type=not_reviewed fail_open=false` | balanced 下 natural runes < 120 不调用；需要覆盖就降低 `QUALITY_GATE_MIN_CRITIC_RUNES`（会增成本） |
| economy 模式 | 规则无发现时 `critic_called=false` | 改回 `QUALITY_GATE_MODE=balanced` |
| 规则无信号且短 | `rule_types=[]`、`natural_runes` 小 | 同上；规则只覆盖可机械识别的类别 |
| DSH observe 模式 | `crisp_quality_gate_active mode=observe`；只记录不改会话 | 配置 `mode: guard` 并重启 profile |
| opencode `rewrite:false` / 无 TUI | `rewrite_requested=false`；headless 无 toast | 需要重写就设 `rewrite: true`；headless 下 toast 本就不可见 |
| 插件看不到日志 | 无 `crisp_quality_check` / `quality_check` 行 | 检查安装/重启/日志路径（第 8、9 节） |

判读 `fail_open=true`：它表示"检查系统内部失败，因此按 PASS 放行"，**不是**质量结论。出现即说明 Critic 或配置有问题，应按第 6 节排查；不要把 `fail_open=true` 当作"回答合格"的证据。

本地验证 Critic 是否真的能产生 block（使用仓库自带 mock，不需要真实 Key）：

```powershell
go build -o .tmp\bin\mockcritic.exe ./test/e2e/mockcritic
.\tmp\bin\mockcritic.exe -addr 127.0.0.1:19001 -verdict block      # -h 查看 -verdict/-delay/-marker/-log
$env:QUALITY_GATE_BASE_URL = 'http://127.0.0.1:19001/v1'
$env:QUALITY_GATE_MODEL    = 'mock-model'
'{"session_id":"s","stop_hook_active":false,"last_assistant_message":"<一段满足长度或含重复句的回答>"}' | .\bin\crisp.exe
# 期望输出 {"decision":"block","reason":"请重新输出完整最终回答…"}
```

## 5. 误拦截 / 过严

### 5.1 先理解判定链

规则 findings（material 也只是信号）→ Critic 语义确认 → 只有 Critic 返回 `decision=block + severity=material` 才会 BLOCK。规则单独永远不会拦截；因此"过严"几乎总是
Critic 判定或 prompt 问题，而不是规则直接造成。

### 5.2 复现与定位

```powershell
# 1) 对原始回答直接跑流水线（check-text 不读 transcript，user_message 为空，与 Hook 有差异）
Get-Content testdata\cases\positive.jsonl | ForEach-Object { ($_ | ConvertFrom-Json).answer } |
  Select-Object -First 1 | .\bin\crisp.exe check-text

# 2) 想看到真实 block，需要配置 Critic 或用第 4 节的 mockcritic（-verdict block）
```

输出中的 `rule_types` / `rule_material` 看规则命中了什么，`reason_type` 看 Critic 的 issues 类型；Hook 模式下再结合 `quality_check` 的 `critic_called`、`critic_latency_ms`。

### 5.3 可调项（按推荐顺序）

| 目标 | 文件 / 配置 | 说明 |
| --- | --- | --- |
| 提高 Critic 判定门槛 | `internal/critic/prompt.go` 的 `systemPrompt` | 收紧 material 定义、补充"不要拦截"的边界；改动后跑回归 |
| 降低重复误报 | `internal/checker/checker.go` 阈值常量 | `duplicateSimilarity`、`minParagraphRunes`、`nearDuplicateParagraphRunes` 等集中在此 |
| 调整客套语表 | `internal/checker/filler.go` | 只改短语表即可，新增场景补样本 |
| 调整调用策略 | `internal/policy/decision.go` 与 env | `MinCriticRunes`（默认 120）、`QUALITY_GATE_MODE` |
| 换更稳/更强的模型 | `QUALITY_GATE_MODEL` | 温度固定 0、不重试；模型变更是判定差异的主要来源 |
| 固定重写指令 | `internal/policy/decision.go` 的 `RewriteReason` | 必须与 `integrations/dsh-plugin-crisp/lib/config.js`、`integrations/opencode-crisp/lib/config.js` 逐字一致（DSH contract 测试会校验） |

修改后至少跑：

```powershell
go test ./...                                             # 含 Hook/决策/日志回归
go test ./cmd/crisp -run TestRuleEvaluation -count=1 -v   # 正常样本必须仍是 0/10 误杀
Push-Location integrations/dsh-plugin-crisp; node --test; Pop-Location
```

注意：`crisp check-text` 不读取 transcript 用户消息，Critic 上下文比 Hook 模式少；复现 Hook 判定时应使用带 `transcript_path` 的完整 payload。

## 6. 慢 / Critic 超时

预算关系（见 README 与 docs/design.md 第 10 节）：

| 项 | 默认 | 环境变量 / 字段 |
| --- | --- | --- |
| Critic 单次 HTTP 超时 | 8s | `QUALITY_GATE_CRITIC_TIMEOUT_MS` |
| Critic 请求额外字段 | 空 | `QUALITY_GATE_EXTRA_BODY_JSON`（如 `{"thinking":{"type":"disabled"}}`） |
| Crisp 进程总预算 | 25s | `QUALITY_GATE_TOTAL_TIMEOUT_MS` |
| 宿主 Hook 截止时间 | 30s | Codex `hooks.json` `timeout` / Claude `timeout` |
| 插件子进程超时 | 30s | DSH/opencode `timeoutMs` |

排查步骤：

1. 看日志：`quality_check` 的 `critic_called`、`critic_latency_ms`、`elapsed_ms`；失败时看 `critic_failed` 的 `error`（超时信息包含配置的超时值；非 2xx 会带 HTTP 状态与响应体片段）。
2. 确认预算没被调反：Critic 超时必须小于进程总预算，进程总预算必须小于宿主 timeout；插件 `timeoutMs` 应高于 Crisp 自身总预算，否则插件会先杀掉 Crisp。
3. 网络/服务：用 `Invoke-WebRequest` 或 curl 直接请求 `{QUALITY_GATE_BASE_URL}/chat/completions` 验证 URL、模型名与鉴权；非 2xx 时优先修服务端。
4. 本地复现超时（预期 fail-open）：

```powershell
.\tmp\bin\mockcritic.exe -addr 127.0.0.1:19002 -verdict block -delay 2s
$env:QUALITY_GATE_BASE_URL = 'http://127.0.0.1:19002/v1'; $env:QUALITY_GATE_MODEL='mock-model'
$env:QUALITY_GATE_CRITIC_TIMEOUT_MS = '300'
'{"session_id":"s","stop_hook_active":false,"last_assistant_message":"<长回答>"}' | .\bin\crisp.exe
# 期望 {}，stderr 出现 critic_failed（超时）且 quality_check fail_open=true
```

5. 降级策略：Crisp 在 Critic 失败时 fail-open（不拦截），不会拖住宿主超过总预算；若审查整体过慢，可缩短 `QUALITY_GATE_CRITIC_TIMEOUT_MS`、降低最大回答长度（插件有 `maxRunes`）、或换低延迟模型。**不要**把宿主 timeout 调小来"杀 Crisp"：Windows 上 Codex 只丢弃输出、不终止进程（第 10 节）。

## 7. check-file 问题

```text
crisp check-file [--json] <file> [...]    # "-" 表示从 stdin 读一个输入
```

- 退出码：`0` 全部通过（含 fail-open）；`1` 至少一个文件 BLOCK；`2` 用法错误，或存在无法读取/检查的文件。
- 每个文件最多读 4 MiB；超过或目录/不存在都按读取失败记录（该文件计为 error），其他文件继续检查。
- `--json` 输出单个对象：`{"files":[...],"summary":{"checked":..,"blocked":..,"failed_open":..,"errors":..}}`。
- 选项可出现在路径前后；`--` 结束选项解析；`-` 只能使用一次（第二次报错并以 2 退出）。
- 未配置 Critic 时：规则发现会让长文件走 Critic → 未配置 → `fail_open=true` 且退出码仍为 0。这是预期行为，不是 bug；配置 Critic 后才能得到语义结论。

示例（本机实测）：

```powershell
.\bin\crisp.exe check-file README.md
# README.md: PASS (reason_type=critic_error; rules=duplicate_sentence,length_signal; critic=fail-open; runes=9313; 1ms)
# checked=1 blocked=0 fail_open=1 errors=0

.\bin\crisp.exe check-file --json docs\design.md
Get-Content README.md -Raw | .\bin\crisp.exe check-file -
.\bin\crisp.exe check-file .tmp\tiny.md --json      # 选项在路径后同样有效
```

## 8. DSH 插件问题

### 8.1 确认已安装并挂载

1. 版本：`dsh --version`（本仓库验证环境为 0.2.1-rc.5）。
2. 安装路径：本仓库推荐 Plugin Manager 的 `install_bundle`（target 为包的绝对目录，如 `D:\05_Code\Crisp\integrations\dsh-plugin-crisp`）。该路径在仓库验证文档中记录为"文档说明"，本机未执行；已实际执行过的是 CLI 路径（隔离 `DSH_HOME`）：

```powershell
$env:DSH_HOME = 'D:\05_Code\Crisp\.tmp\dsh-home'
$dsh = 'C:\Users\71408\AppData\Local\Programs\DeepSeek Harness\resources\runtime\cli\bin\dsh.cmd'
& $dsh plugin --profile crisp-scratch add 'D:\05_Code\Crisp\integrations\dsh-plugin-crisp'
& $dsh --profile crisp-scratch --dump-config | Select-String 'crisp-quality-gate' -Context 0,15
```


看到 `crisp-quality-gate` 行与 `config:` 才说明 bundle 层已合成；安装/改配置后需要重启 profile。
3. 激活日志：应出现 `crisp_quality_gate_active`，含 `mode`、`binary`、`binary_exists`、`timeout_ms`、`log_file`。`binary_exists:false` 或 `binary` 不对时，配置 `binaryPath`、`CRISP_BIN` 或先构建 `bin/crisp.exe`。

### 8.2 日志看不到

- 默认 `logFile` 为空 → JSONL 写到 Harness 进程的 stderr；在 Web GUI 里可能拿不到。配置 `logFile: 'D:\path\crisp-dsh.jsonl'` 后重启 profile，直接读该文件。
- 先确认 `enabled` 未被 `CRISP_DSH_DISABLE=1` 关闭，`mode` 是否为 `observe`（observe 只记录、不改会话）。

### 8.3 没有检查记录 / 总是 pass

- 回答太短：插件 `minRunes` 默认 40（先于 Go 的 120 生效），日志为 `crisp_quality_skip` + `skip_reason=short_answer`。
- 工具调用/非正常结束：`crisp_quality_skip` 的 `skip_reason` 可能是 `finish_error`、`finish_max-tokens`、`finish_aborted`、`missing_finish`。
- `QUALITY_GATE_*` 没进子进程：DSH 插件只传环境白名单 + `envPassthrough` + `env` 覆盖；写在 profile `.env` 里的变量不一定在 `process.env`。请在 Harness 进程环境或插件 `env` 配置里设置。
- guard 不重写：确认 `mode: guard`；日志 `crisp_guard_skipped` 会给出 `no_turn`、`rewrite_turn`、`already_rewritten`、`agent_unavailable`、`followup_unavailable` 等原因。
- 语义提醒：DSH guard 是**后置**修复，原回答已经交付，重写是新 turn；不是交付前拦截。

## 9. opencode 插件问题

1. 版本与安装：`opencode --version`（本仓库验证为 1.18.18）。本地插件用 `opencode.json` 的 `plugin` 数组 + 文件 URL（Windows 用正斜杠），例如 `file:///D:/05_Code/Crisp/integrations/opencode-crisp/index.js`。`opencode plugin <module>` 只接受 npm 包名（帮助文本为 "npm module name"），不适用于本地目录。
2. 是否被加载：`opencode debug info` 的 plugins 列表、`opencode debug config` 的 `plugin_origins`；两者只证明配置被解析，是否真正加载以决策日志为准。
3. 加载报错 `Plugin export is not a function`：入口必须只导出函数（本包 `index.js` 只有默认导出）。不要加非函数的命名导出。
4. 完全没日志：
   - 是否用了 `--pure` 或 `OPENCODE_PURE=1`（禁用所有外部插件）；
   - 检查 `opencode debug paths` 给出的数据/日志目录下 opencode 自身日志中的 error；
   - 插件 JSONL 默认写在系统临时目录 `opencode-crisp/quality-gate.jsonl`，也可用 tuple options 的 `logFile` 指定；同时会写 opencode 服务端日志（service 名 `crisp`）。
5. `opencode run` 下没有检查：事件钩子是 fire-and-forget，一次性进程可能在异步检查完成前退出。用 `opencode serve`、TUI 或长驻 server 验证。
6. 决策恒为 `fail_open=true`：检查 `crispBin` 路径/权限；先手工运行 `crisp.exe check-text`；看 JSONL 的 `error` 字段。
7. toast 看不到：headless（`run`/`serve` 无 TUI）或 `toast:false`；headless 下调用被忽略是预期行为。
8. 不想追加重写：`"rewrite": false`（只保留 toast/日志）；完全关闭：`CRISP_OPENCODE_DISABLE=1` 或从 `plugin` 数组移除。配置改动后需重启 opencode。
9. 重写成本：每次 block 会多一次模型调用和一条新的用户消息 + assistant 回答；原回答仍留在会话中。

## 10. Windows 专有问题

1. Codex 超时不杀 Hook 进程：实测 `timeout: 2` 配 4s/8s 的慢 Hook 时，Codex 标记 `hook: Stop Failed` 并丢弃输出，但 Hook 进程继续跑到结束。因此不要依赖宿主 timeout 清理卡住的 Crisp；Crisp 自身 25s 总预算负责退出。若父进程被强杀，可能留下孤立的 Hook 进程，需要手工清理。
2. PowerShell 引号与路径：Claude handler 使用 `& '<绝对路径>'` + `"shell": "powershell"`；Codex 的 `command` 是"可执行文件 + 参数"（不经过 shell），仓库配置使用不含空格的绝对路径；含空格路径在本仓库未验证，使用前先手工验证。
3. Git Bash 默认 shell：Claude Code 在装有 Git Bash 时默认用 bash，Windows 路径会被改写；务必保留 `"shell": "powershell"`。
4. BOM：带 UTF-8 BOM 的 Hook stdin 会被判为非法 JSON（`hook_input_parse_failed`），按 fail-open 输出 `{}`。写入配置/测试文件时用无 BOM UTF-8：`[System.IO.File]::WriteAllText($p, $text, (New-Object System.Text.UTF8Encoding($false)))`（仓库脚本均如此）；PowerShell 5.1 的 `Set-Content -Encoding UTF8` 会带 BOM，注意区分。
5. CRLF：Crisp 会规范 `\r\n`/`\r` 行尾，Hook JSON 尾随 CRLF 也被容忍；CRLF 输入本身不是问题。
6. JSON 中的 Windows 路径要转义反斜杠（`\\` 或正斜杠），否则 JSON 解析失败 → fail-open。
7. 子进程可见性：两个插件都用 `windowsHide: true` 启动 Crisp，不会弹窗；超时用 `child.kill()` 终止直接子进程。

## 11. 诊断信息收集清单

提交问题前收集（不含 API Key、不粘贴完整回答）：

```powershell
# 版本
.\bin\crisp.exe version
go version; node --version
# 宿主版本（按已安装的取）
codex --version; claude --version; dsh --version; opencode --version

# 测试与协议回放
go test ./...
go run ./test/e2e/hookreplay -crisp bin/crisp.exe -v

# 相关配置（注意脱敏）
Get-Content .codex\hooks.json -Raw
Get-Content .claude\settings.json -Raw
Get-Content .\opencode.json -Raw -ErrorAction SilentlyContinue

# 日志（按接入方式取其一）
Get-Content <crisp-stderr.log> -Tail 100
Get-Content <crisp-dsh.jsonl> -Tail 100
Get-Content <quality-gate.jsonl> -Tail 100
```

同时在报告中说明：操作系统与 shell、宿主及版本、使用的是哪条接入路径（Stop Hook / DSH / opencode）、是否配置了真实 Critic（只说明"是/否"与模型名，不给 Key）、复现命令与完整 stderr/JSONL 行。
