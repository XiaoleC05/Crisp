# opencode 接入：Crisp Quality Gate 插件

状态：**已在 opencode 1.18.18 上端到端验证**（隔离配置 + 本地 mock provider + 本地 mock critic）。
插件实现与单元测试见 `integrations/opencode-crisp/`；本文件记录验证过的插件 API、安装方式、配置、
行为、已验证 / 未验证项、限制与数据边界。

- 验证日期：2026-11-02（Asia/Shanghai）
- opencode：`1.18.18`（`D:\01_DevTools\nodeJS\node_modules\opencode-ai\bin\opencode.exe`）
- Crisp 二进制：`bin/crisp.exe`（`go build -o bin/crisp.exe ./cmd/crisp`）
- 宿主插件类型定义：`C:\Users\71408\.config\opencode\node_modules\@opencode-ai\plugin@1.17.18`（只读参考；
  该版本与运行时 1.18.18 有少量字段差异，本实现只依赖运行时行为与 SDK client 的 HTTP 接口）

---

## 1. 验证摘要

| # | 结论 | 证据 |
|---|------|------|
| 1 | 插件能被 1.18.18 加载，factory 被调用且 tuple options 生效 | 隔离环境中 `opencode serve` + 诊断插件记录 `FACTORY dir=...`；JSONL 决策日志写到了 tuple options 指定的 `logFile` 路径 |
| 2 | `event` hook 收到 bus 事件（`session.idle` 等） | 隔离环境诊断插件逐事件写日志；`session.idle props={"sessionID":"ses_..."}` |
| 3 | `session.idle` 后插件能取到最后一条 assistant 文本并调用 `crisp check-text` | E2E 决策日志 `decision=pass answer_runes=30 exit_code=0 elapsed_ms=21` |
| 4 | block 决策可端到端产生（mock critic 返回 material） | `decision=block reason_type=semantic_duplicate critic_called=true`；mock critic 收到 1 次带 Bearer 的 POST |
| 5 | 一次重写请求可端到端发出，且不会再次审查重写结果 | mock provider 收到 2 次 chat completions；日志出现 `rewrite_sent=true`，随后 `event=quality_skip skip_reason=rewrite_result` |
| 6 | 结构化 JSON 行同时写入 JSONL 文件与 opencode 服务端日志（app.log） | 文件首行与 `serve.log` 中 `level=WARN ... event=quality_check` 行一致 |
| 7 | 🚫 不修改用户真实 opencode 配置 | 全部 E2E 使用隔离 `HOME` / `XDG_CONFIG_HOME` / `XDG_DATA_HOME` / `XDG_CACHE_HOME` / `XDG_STATE_HOME`；用户目录只读 |

## 2. 已验证的插件 API（opencode 1.18.18）

以下全部来自运行时二进制的插件加载逻辑、`@opencode-ai/plugin` / `@opencode-ai/sdk` 类型定义，
以及隔离环境中的真实加载与事件日志。

### 2.1 模块加载协议

opencode 的插件加载器会遍历模块命名空间的 **所有导出值**：

```js
// 运行时逻辑（二进制内简化后的等价代码）
function isFn(v) { return typeof v === "function"; }
function pick(v) { return isFn(v) ? v : (v && typeof v === "object" && isFn(v.server) ? v.server : undefined); }
function collect(namespace) {
  const out = [];
  for (const v of Object.values(namespace)) {
    const fn = pick(v);
    if (!fn) throw new TypeError("Plugin export is not a function");
    out.push(fn);
  }
  return out;
}
```

结论（硬约束，`index.js` 已遵守）：

- 插件入口必须导出 **函数**（默认导出即可），或导出 `{ server: fn }`；任何非函数的命名导出都会让
  整个插件加载失败，报 `Plugin export is not a function`。
- 因此本包 `index.js` 只有 **一个** 默认导出；工具函数都在 `lib/` 中。
- factory 签名：`(input, options) => Promise<Hooks>`，opencode 在 server 启动时调用一次。
- `input` 包含 `{ client, project, worktree, directory, serverUrl, $, experimental_workspace }`；
  其中 `client` 是 `@opencode-ai/sdk` 的 client，可直接调用 HTTP API。
- `options` 来自配置里的 tuple 形式 `["spec", {...}]`。

### 2.2 配置里的 plugin 字段

`plugin` 是数组，每一项可以是：

- npm 包名（可带版本）：`"opencode-gemini-auth"`、`"pkg@1.2.3"`
- 相对配置文件的路径：`"./local-plugin.ts"`
- 绝对 file URL：`"file:///D:/05_Code/Crisp/integrations/opencode-crisp/index.js"`
- tuple：`["spec", { "rewrite": true }]`（options 会作为 factory 第二个参数）

opencode 1.18.18 的 `debug config` 会把它解析成 `plugin_origins`（已实测）：
`opencode plugin <module>` 子命令只安装 **npm 包**（其帮助文本为 `"npm module name"`），
不适用于本地目录插件；本地插件请直接写配置。另外支持自动发现 `.opencode/plugin/*.js`，
但本包入口依赖相对导入 `./lib/`，自动发现只加载单个文件，因此推荐 file URL 方式。

其他已验证的逃生开关（来自 1.18.18 内置文档）：`OPENCODE_CONFIG`、`OPENCODE_CONFIG_CONTENT`、
`OPENCODE_PURE=1` / `--pure`（禁用所有外部插件）、`OPENCODE_DISABLE_PROJECT_CONFIG`。

### 2.3 Hooks 与事件

`Hooks` 支持的钩子（本实现只使用 `event`）：`event`、`config`、`chat.message`、
`chat.params`、`chat.headers`、`tool.execute.before/after`、`tool.definition`、
`command.execute.before`、`shell.env`、`permission.ask`、`experimental.*`。

重要运行时细节（已从二进制确认并实测）：

- `event(input)` 收到 `{ event: { id, type, properties } }`，且事件分发是 **fire-and-forget**：
  宿主的 bus listener 里用 `v.sync(() => hooks.forEach(h => h.event?.({event})))` 调用，
  **不 await** 返回的 Promise，也不把异常回传给插件之外的调用方。
- 其他钩子（如 `chat.message`）通过 `Plugin.trigger` 逐个 `await`。
- 插件实例只收到 `event.location.directory === plugin.directory` 的事件；跨目录的 session 不会到达。
- 事件 payload 例子（实测）：
  ```text
  { "id": "...", "type": "session.idle", "properties": { "sessionID": "ses_..." } }
  { "id": "...", "type": "message.updated", "properties": { "info": { "role": "assistant", "time": { "completed": 1793628676... }, ... } } }
  ```
- 因此本插件用 `session.idle`（每个 turn 结束时必发）触发检查，并用 `message.updated`
  （assistant 且 `time.completed` 已设置）作为补充触发；两者通过 message id 去重。

### 2.4 本实现用到的 client 方法（v1 SDK，HTTP 对应关系已实测）

| client 调用 | HTTP | 用途 |
|---|---|---|
| `client.session.get({ path: { id } })` | `GET /session/{id}` | 判断是否 subagent 子会话（有 `parentID` 时默认跳过） |
| `client.session.messages({ path: { id } })` | `GET /session/{id}/message` | 取 `Array<{ info, parts }>`，提取最后一条可见 assistant 文本 |
| `client.tui.showToast({ body: { title, message, variant, duration } })` | `POST /tui/show-toast` | block 时向 TUI 提示；headless 下失败会被忽略 |
| `client.session.promptAsync({ path: { id }, body: { parts, model, agent } })` | `POST /session/{id}/prompt_async` | 请求 **恰好一次** 重写（追加一个用户 turn） |
| `client.app.log({ body: { service, level, message, extra } })` | `POST /log` | 把同一条 JSON 决策写进 opencode 服务端日志 |

SDK 返回值形态为 `{ data }`（或 `{ error }`）；`lib/hooks.js` 的 `unwrapResult`
同时兼容裸数据，便于单元测试替身。

## 3. 行为

### 3.1 触发与取文本

1. 监听 `session.idle` / assistant `message.updated`；同一 session 的检查串行化（in-flight 去重）。
2. `session.get`：默认跳过子会话（`includeSubagents: true` 可放开）。
3. `session.messages`：从末尾向前找最后一条 assistant，跳过 `error`、`summary`、空文本；
   拼接所有可见 text part（跳过 `synthetic` / `ignored` / reasoning / tool part）。
4. 同一 assistant message 只审查一次（`session.idle` 与 `message.updated` 会重复触发）。

### 3.2 调用 Crisp

把回答全文写到 `crisp check-text` 的 stdin，读取 stdout 的一个 JSON 摘要
（`decision`、`reason_type`、`rule_types`、`critic_called`、`fail_open`、`answer_runes`…），
子进程环境继承 opencode 进程的环境（因此 `QUALITY_GATE_*` 原样传递），另外可用 tuple options 的
`env` / `mode` 覆盖。超时（默认 30s，范围 1–120s）到点 kill 子进程。

**一切异常 fail-open**：二进制不存在、spawn 失败、非 0 退出、输出非法 JSON、超时、stdout 为空
→ 记为 `decision=pass fail_open=true`，不提示、不重写。与 Go 核心的 fail-open 语义一致。

### 3.3 block 时的用户可见输出与重写

opencode 没有 Codex 的 Stop-hook “block 并让模型重新生成完整回答”协议：回答此时已经流式写入会话。
因此插件做两件事：

1. **TUI toast**（`toast !== false` 且 TUI 在线时）：
   `Crisp quality gate` + `Crisp: 检测到实质质量问题（<reason_type>）…`，variant `warning`。
   headless（`opencode run` / `serve` 无 TUI）下调用失败会被静默忽略。
2. **恰好一次重写**（`rewrite !== false` 且 `session.promptAsync` 可用时）：
   向该 session 追加一个用户 turn，内容为与 `internal/policy/decision.go` 中
   `policy.RewriteReason` **逐字一致** 的固定指令：
   `请重新输出完整最终回答：先在第一段给出直接结论，再按清晰结构展开；合并语义重复的段落，删除无效铺垫和结论重复，保留全部必要事实、数字、代码和解释，不要为了缩短篇幅而删除必要信息。`
   同时带上原回答的 `model` / `agent`，确保同一模型重写。

一次重写守卫（对应 Codex 的 `stop_hook_active`，见 docs/design.md 6.4）：

- `checked`：已审查的 assistant message id 集合（防重复审查）。
- `pendingRewrite`：请求重写后，该 session 的下一条 assistant message 视为“重写结果”，
  **不再审查**，并记 `skip_reason: "rewrite_result"`。
- `rewrittenTurns`：以 assistant 的 `parentID`（即用户 turn）为键，保证同一 turn 最多一次重写。
- 若 `promptAsync` 本身失败，回滚 pending 状态，允许后续 turn 再次尝试；日志记 `rewrite_error`。

### 3.4 结构化日志（每次检查一行 JSON）

写入 `logFile`（默认 `<系统临时目录>/opencode-crisp/quality-gate.jsonl`，可置 `false` 关闭），
并通过 `client.app.log` 写进 opencode 服务端日志。真实 E2E 中的一行（pass 场景）：

```json
{"ts":"2026-11-02T14:12:11.595Z","level":"info","event":"quality_check","source":"opencode-crisp","session_id":"ses_e6365904bffex4zNldUBnirGy8","message_id":"msg_19c9a72200015fcBGJqzC3ZGZz","parent_message_id":"msg_19c9a6fec001jM69zZNnZAfkSD","agent":"build","decision":"pass","reason_type":"not_reviewed","rule_types":[],"critic_called":false,"fail_open":false,"answer_runes":30,"natural_runes":30,"timeout_ms":30000,"timed_out":false,"exit_code":0,"elapsed_ms":21,"rewrite_requested":false,"rewrite_sent":false}
```

block 场景（同一 E2E，mock critic 返回 material）：

```json
{"event":"quality_check","decision":"block","reason_type":"semantic_duplicate","fail_open":false,"answer_runes":399,"message_id":"msg_19c9ae753001Mv8jHJ3LUxsXhR","rewrite_requested":true}
{"event":"quality_skip","message_id":"msg_19c9aee350011i0N7rtJK85CY2","skip_reason":"rewrite_result"}
```

日志**不包含**回答全文、用户消息或 API Key；只含 id、决策枚举、计数、耗时、错误字符串。

## 4. 安装

### 4.1 前置

```powershell
cd D:\05_Code\Crisp
go build -o bin\crisp.exe ./cmd/crisp
.\bin\crisp.exe check-text   # 输入一段文本后回车（Ctrl+Z 回车结束 stdin），应输出 JSON
```

### 4.2 写配置

编辑全局 `~/.config/opencode/opencode.json`（或项目级 `opencode.json`）。Windows 上 file URL 用正斜杠：

```json
{
  "$schema": "https://opencode.ai/config.json",
  "plugin": [
    [
      "file:///D:/05_Code/Crisp/integrations/opencode-crisp/index.js",
      {
        "crispBin": "D:/05_Code/Crisp/bin/crisp.exe",
        "rewrite": true,
        "toast": true,
        "timeoutMs": 30000,
        "logFile": "D:/05_Code/Crisp/.tmp/opencode-crisp/quality-gate.jsonl"
      }
    ]
  ]
}
```

Critic 配置依旧走环境变量（由插件启动的 Crisp 子进程继承）：

```powershell
$env:QUALITY_GATE_BASE_URL = "https://api.example.com/v1"
$env:QUALITY_GATE_API_KEY  = "sk-..."      # 不要提交到仓库
$env:QUALITY_GATE_MODEL    = "your-light-model"
$env:QUALITY_GATE_MODE     = "balanced"
```

配置改完后需要重启 opencode；运行中的会话继续使用已加载的配置。

### 4.3 验证插件已被识别

```powershell
opencode debug info    # plugins 列表应包含 file:///.../integrations/opencode-crisp/index.js
opencode debug config  # plugin_origins 中应出现 tuple（含 options）
```

`debug info` 只证明配置被解析；是否真正加载以决策日志 / 服务端日志为准。

## 5. 配置参考（tuple options 与专用环境变量）

| 选项 | 类型 / 默认 | 说明 |
|---|---|---|
| `crispBin` | string，默认仓库 `bin/crisp[.exe]` | Crisp 可执行文件；优先级 `options.env.CRISP_BIN > options.crispBin > env.CRISP_BIN > 仓库默认` |
| `timeoutMs` | number，30000（1s–120s） | 单次 `crisp check-text` 子进程硬超时 |
| `rewrite` | boolean，true | block 时是否用 `promptAsync` 请求一次重写 |
| `toast` | boolean，true | block 时是否尝试 TUI toast |
| `appLog` | boolean，true | 是否同时写 opencode 服务端日志（`app.log`） |
| `logFile` | string 或 `false` | JSONL 决策日志路径；默认系统临时目录下 |
| `mode` | "balanced" / "economy" | 映射为子进程 `QUALITY_GATE_MODE` |
| `env` | object | 追加/覆盖 Crisp 子进程环境变量；值为 `undefined` 时删除该项 |
| `includeSubagents` | boolean，false | 是否审查 subagent 子会话（默认跳过） |
| `maxChecked` | number，500 | `checked` / `rewrittenTurns` 内存去重集合上限 |
| `rewriteInstruction` | string | 覆盖固定重写指令（默认与 Go policy 完全一致，不建议改） |

专用环境变量：

| 环境变量 | 说明 |
|---|---|
| `CRISP_BIN` | 同 `crispBin` |
| `CRISP_TIMEOUT_MS` | 同 `timeoutMs` |
| `CRISP_OPENCODE_LOG_FILE` | 同 `logFile` |
| `CRISP_OPENCODE_DISABLE=1` | 插件注册空 hooks（完全关闭） |
| `QUALITY_GATE_*` | 原样传给 Crisp 子进程（Critic 配置、模式、超时等，见 README） |

## 6. 已验证 vs 未验证

**已验证（真实进程、隔离配置、无外网 provider）**

1. opencode 1.18.18 从 `opencode.json` 的 tuple + file URL 加载本插件，factory 与 `event` hook 均被调用。
2. `session.idle` 事件可达；插件通过 SDK HTTP 取 transcript 并 spawn `bin/crisp.exe check-text`，
   21ms 内完成，写出 pass 决策（`answer_runes=30`，与 mock provider 的回答一致）。
3. mock critic 返回 `{decision:block, severity:material}` 时，Crisp 决策为 block（`critic_called=true`），
   插件写 block 日志并调用 `session.promptAsync`；mock provider 因此收到第二次 chat completions；
   重写结果消息随后被 `skip_reason=rewrite_result` 跳过 → 一次重写守卫生效。
4. 同一条 JSON 决策行同时出现在 JSONL 文件与 opencode 服务端日志（`app.log`）。
5. 单元测试 51 个全部通过（含用真实 `bin/crisp.exe` 的 hermetic 集成用例）。

**未验证 / 需要真机补充**

1. **TUI toast 的实际渲染**：E2E 用 headless `opencode serve`，toast 调用被忽略（预期）；TUI 下
   `tui.showToast` 的效果尚未人工确认。
2. **`opencode run` 一次性模式**：事件 hook 确认被调用（诊断插件日志），但异步多步检查在进程退出前
   未观察到完成（未写出决策日志）。见限制 7.1。
3. 自动发现目录 `.opencode/plugin/*.js` 未单独测试（本包推荐显式 file URL）。
4. npm 发布 / `opencode plugin <module>` 安装路径未测试（本包为 private 本地包）。
5. 真实模型 / 真实 Critic 服务未参与验证（只用本地 mock，符合“无真实网络”要求）。
6. Windows 之外平台未运行（代码按平台选择 `crisp` / `crisp.exe` 默认路径，但未实测）。

## 7. 已知限制

### 7.1 `opencode run`（一次性、非 TUI）可能丢检查

宿主的 `event` hook 是 fire-and-forget（不 await）。`opencode run` 在 `session.idle` 后立刻销毁
instance 并退出进程；插件此时可能仍在做 `session.messages` → spawn Crisp 的异步流程。
本机实测：`opencode serve`（长驻）100% 写出决策日志；`opencode run` 未写出（诊断插件证明事件确实到达）。
因此：

- 用于 TUI / `opencode serve` / 长驻 server 是可靠的；
- 若必须覆盖 `opencode run`，需要 opencode 提供可 await 的完成钩子，或由外层包装等待实例空闲；
  当前实现不宣称覆盖该模式。

### 7.2 无法真正“拦截”回答

opencode 没有 Codex Stop hook 那种“block → 平台让模型重写”的协议；回答已经写进会话并显示。
本插件的语义是 **观察 + 提示 + 追加一次完整重写 turn**，而不是拦截原回答。原回答仍留在会话历史中。

### 7.3 重写的代价与形态

开启 `rewrite` 后 block 会多一次模型调用（额外 token 成本与延迟），且在会话里表现为一条新的用户消息
（重写指令）+ assistant 回答。若不想自动追加 turn，把 `rewrite` 设为 `false`，仅保留 toast / 日志。

### 7.4 状态在内存里

去重 / 一次重写守卫只存在于当前 server 进程内：重启 opencode 后清空；同一会话在不同进程间不共享。
不会写任何 opencode 会话数据库或用户配置。

### 7.5 子会话与目录过滤

默认跳过 subagent 子会话（`parentID` 非空）。只处理与插件同目录（`event.location.directory`）的会话；
多 project / 多 worktree 各自实例独立。

### 7.6 不改变回答内容

除“追加一次重写 turn”外，插件不修改任何消息或 part；没有使用 `experimental.text.complete`
（该钩子可在 text-end 时改写文本，但改写不属于 Crisp 的职责，且属于实验 API）。

## 8. 数据边界

- 回答全文只通过 **本地 stdin** 交给 `crisp check-text` 子进程；
- 若配置了 `QUALITY_GATE_BASE_URL`，Crisp 会把回答全文、可选用户上下文（≤2000 字符）和规则发现摘要
  发送给该 Critic 服务——这与 Codex 接入的数据边界完全相同，是否允许外发取决于所选服务与合规要求；
- `QUALITY_GATE_API_KEY` 只在子进程环境与 Critic 请求头中使用，不写日志、不入库；
- 插件日志（JSONL / app.log）只含 session/message id、决策枚举、计数、耗时、错误字符串；
  **不含回答全文、用户消息、API Key**；
- 插件不读取 / 不修改 opencode 会话数据库、用户配置或凭证；唯一写入是自选的 JSONL 日志文件。

## 9. 测试

### 9.1 单元测试（hermetic，无网络）

```powershell
cd integrations\opencode-crisp
node --test
```

当前结果：**51 tests / 51 pass / 0 fail**（2026-11-02，Node v22.19.0）。覆盖：

- 配置默认值、优先级、超时钳制、mode/env 映射、禁用开关（`test/config.test.js`）
- 事件与 assistant 文本提取、synthetic/ignored/error/summary 过滤（`test/events.test.js`）
- check-text 调用：pass/block/非法 JSON/非 0 退出/超时 kill/spawn 错误/env 传递（`test/crisp.test.js`，
  含真实 `spawn` + fake crisp 脚本 + 真实 `bin/crisp.exe` 的用例）
- 一次重写守卫、rewrite 结果跳过、回滚、集合上限（`test/guard.test.js`）
- 插件端到端（fake client/events/spawn）：pass、block+toast+promptAsync、重复事件去重、rewrite 结果跳过、
  fail-open、subagent 跳过、toast 失败、logger 失败、真实 `bin/crisp.exe`（`test/plugin.test.js`）
- 结构化日志写入与失败吞掉（`test/log.test.js`）

### 9.2 真实 opencode E2E（隔离配置，本地 mock）

harness 在 `integrations/opencode-crisp/e2e/`（`mock-provider.cjs`、`mock-critic.cjs`、`drive.cjs`）。
2026-11-02 实际执行的步骤（Windows PowerShell；全部路径在仓库 `.tmp` 下，不触碰用户配置）：

```powershell
$repo = "D:\05_Code\Crisp"
$e2e  = "$repo\.tmp\oc-e2e"
New-Item -ItemType Directory -Force -Path "$e2e\config\opencode", "$e2e\work" | Out-Null

# 隔离的 opencode 全局目录（HOME/USERPROFILE + XDG_*）
$env:HOME="$e2e"; $env:USERPROFILE="$e2e"
$env:XDG_CONFIG_HOME="$e2e\config"; $env:XDG_DATA_HOME="$e2e\data"
$env:XDG_CACHE_HOME="$e2e\cache"; $env:XDG_STATE_HOME="$e2e\state"

# 隔离配置：本地 mock provider + 本插件（含 options）
@'
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "mock": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Mock Local",
      "options": { "baseURL": "http://127.0.0.1:19876/v1" },
      "models": { "mock-model": { "name": "Mock Model" } }
    }
  },
  "model": "mock/mock-model",
  "plugin": [
    ["file:///D:/05_Code/Crisp/integrations/opencode-crisp/index.js",
     { "logFile": "D:/05_Code/Crisp/.tmp/oc-e2e/crisp-quality.jsonl" }]
  ]
}
'@ | Set-Content "$e2e\config\opencode\opencode.json" -Encoding UTF8

# 启动 opencode serve（长驻模式，插件事件才能完成异步检查）
Set-Location "$e2e\work"
Start-Process -FilePath "D:\01_DevTools\nodeJS\node_modules\opencode-ai\bin\opencode.exe" `
  -ArgumentList "serve","--port","19877","--hostname","127.0.0.1","--print-logs","--log-level","INFO" `
  -RedirectStandardOutput "$e2e\serve.log" -RedirectStandardError "$e2e\serve.err"

# 启动 mock provider（answer 可通过 MOCK_ANSWER 覆盖）
Start-Process -FilePath "node" -ArgumentList "$repo\integrations\opencode-crisp\e2e\mock-provider.cjs" `
  -RedirectStandardError "$e2e\mock-provider.log"

# 驱动：创建 session + prompt，等待 3s，读取 JSONL 决策日志
$env:OC_BASE="http://127.0.0.1:19877"
$env:OC_PROMPT="Say a short sentence."
$env:CRISP_LOG="$e2e\crisp-quality.jsonl"
node "$repo\integrations\opencode-crisp\e2e\drive.cjs"
```

实测输出（pass 场景）：

```text
server ready
session: ses_e6365904bffex4zNldUBnirGy8
prompt status: 200
assistant text: "This is a normal short answer."
crisp log lines: 1
LOG {"event":"quality_check","decision":"pass","reason_type":"not_reviewed","fail_open":false,"answer_runes":30,"message_id":"msg_19c9a72200015fcBGJqzC3ZGZ","rewrite_requested":false}
```

block + 重写场景：用 `e2e/mock-critic.cjs`（始终返回 material block）替代 / 追加为 Critic，启动 server 时设置
`QUALITY_GATE_BASE_URL=http://127.0.0.1:19878/v1`、`QUALITY_GATE_MODEL=mock-critic`，
mock provider 用重复长文本（≥120 natural runes）；实测输出：

```text
crisp log lines: 2
LOG {"event":"quality_check","decision":"block","reason_type":"semantic_duplicate","fail_open":false,"answer_runes":399,"message_id":"msg_19c9ae753001Mv8jHJ3LUxsXhR","rewrite_requested":true}
LOG {"event":"quality_skip","message_id":"msg_19c9aee350011i0N7rtJK85CY2","skip_reason":"rewrite_result"}

# mock critic:  [mock-critic] POST /v1/chat/completions body_bytes=4498 auth=true
# mock provider: 2 次 POST /v1/chat/completions（原始回答 + 重写回答）
```

注意：真实 opencode 在隔离 HOME 下首次启动会下载 models.dev 目录与安装
`@opencode-ai/plugin@1.18.18` 到 `XDG_CONFIG_HOME/opencode/node_modules`（本机实测可完成）。
本次 E2E 的网络只用于 opencode 自身的依赖安装与 ripgrep 下载；模型与 Critic 均为本地 mock。

## 10. 故障排查

| 现象 | 排查 |
|---|---|
| 决策日志不出现 | 先确认 `opencode debug info` 的 plugins 列表包含本插件；确认不是 `--pure` / `OPENCODE_PURE=1`；再看 `~/.local/share/opencode/log/opencode.log`（路径以 `opencode debug paths` 为准）中的 error |
| `Plugin export is not a function` | 插件入口出现了非函数导出；本包 `index.js` 必须保持只有默认导出 |
| 决策恒为 `fail_open=true` | 检查 `crispBin` 路径、文件权限；直接手动运行 `crisp.exe check-text` 验证；看 JSONL 中的 `error` 字段 |
| Critic 未被调用 | 未配置 `QUALITY_GATE_BASE_URL`/`QUALITY_GATE_MODEL`，或回答 natural runes < `QUALITY_GATE_MIN_CRITIC_RUNES`（默认 120）且规则无发现 |
| toast 看不到 | headless / `opencode run` 无 TUI；`toast:false`；或 TUI 版本 API 差异（见未验证项） |
| 不想让 block 追加重写 | `"rewrite": false` |
| 完全关闭插件 | `"CRISP_OPENCODE_DISABLE": "1"`（临时）或从 `plugin` 数组移除 |

---

## 附：文件清单

```text
integrations/opencode-crisp/
  package.json            private ESM 包，exports 插件入口
  index.js                唯一的默认导出：opencode 插件 factory
  lib/config.js           配置 / 环境变量 / 默认二进制路径 / 固定重写指令
  lib/events.js           事件与 assistant 文本提取（纯函数）
  lib/crisp.js            有界、fail-open 的 crisp check-text 子进程调用
  lib/rewrite-guard.js    一次审查 / 一次重写守卫（opencode 版 stop_hook_active）
  lib/hooks.js            event hook 编排、toast、promptAsync 重写
  lib/log.js              结构化 JSON 行日志（JSONL + app.log）
  test/                   node:test hermetic 单元测试（51 个）
  e2e/                    真实 opencode E2E harness（mock provider / critic / 驱动脚本）
docs/integrations/opencode.md
```
