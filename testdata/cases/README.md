# Crisp 回归样本（testdata/cases）

本目录是 Crisp 输出质量门的回归样本基线，供确定性规则测试、Critic mock 测试和评测脚本读取。
样本的权威定义见 `docs/design.md` 第 12.1 节；本 README 说明当前基线的内容、字段格式和扩展规则。

## 1. 文件与计数

| 文件 | 行数 | 类别与数量 | expected | expected_severity |
| --- | --- | --- | --- | --- |
| `positive.jsonl` | 20 | `duplicate_conclusion` ×5、`filler_opening` ×5、`over_fragmentation` ×5、`redundant_explanation` ×5 | `block` | `material` |
| `negative.jsonl` | 10 | `good_long` ×5、`good_short` ×5 | `pass` | `none` |
| 合计 | 30 | 6 个类别 | — | — |

## 2. 文件格式

- 每行一个 JSON 对象（JSONL），UTF-8 无 BOM，LF 行尾，文件末尾保留一个换行。
- 不允许空行；每行只能是一个完整 JSON 对象，行尾不能有多余字符。
- `answer` 字段内的换行必须写成 JSON 转义序列 `\n`，不允许在 JSON 字符串里直接换行。
- 解析方式与生产一致，使用 Go `encoding/json`；文件必须能逐行反序列化。
- 样本不含真实密钥、Token、个人隐私；示例值均为占位符。

## 3. 字段说明

| 字段 | 类型 | 必填 | 约束与说明 |
| --- | --- | --- | --- |
| `id` | string | 是 | 全局唯一、稳定；格式为 `<类别短横线>-NN`，如 `dup-conclusion-01`；已有 id 不修改、不复用。 |
| `category` | string | 是 | 六个枚举之一：`duplicate_conclusion`、`filler_opening`、`over_fragmentation`、`redundant_explanation`、`good_long`、`good_short`。 |
| `user_message` | string | 是 | 该回答对应的用户原始问题，供 Critic 判断回答是否切题；允许空字符串。 |
| `answer` | string | 是 | 被评价的完整 Codex 风格 Markdown 回答。 |
| `expected` | string | 是 | `block` 或 `pass`。问题样本为 `block`，正常样本为 `pass`。 |
| `expected_severity` | string | 是 | `material` 或 `none`。问题样本为 `material`（规则本身不产生 BLOCK，只有 Critic 判 `block + material` 才真正拦截）；正常样本为 `none`。 |
| `expected_rule_signals` | string[] | 是 | 期望确定性规则至少命中的 finding 类型，取值来自 `internal/checker` 的 `FindingType`：`duplicate_paragraph`、`duplicate_sentence`、`filler_opening`、`filler_closing`、`structure_anomaly`、`length_signal`。正常样本必须为 `[]`。 |
| `notes` | string | 是 | 该样本考察的具体缺陷、预期信号与判定依据，供人工复核与调参。 |

## 4. 类别意图

| 类别 | 预期 | 缺陷定义 | 规则信号 |
| --- | --- | --- | --- |
| `duplicate_conclusion` | block / material | 同一结论换说法在开头、正文、结尾重复 2–3 次。每条样本都含一句 ≥30 字、逐字出现在不同段落中的长句，保证 `duplicate_sentence` 规则可确定性命中（重复句位于代码块之外）。 | `duplicate_sentence`；正文其余内容正常，不混入客套话或过度拆分。 |
| `filler_opening` | block / material | 开头 1–2 段是“好的，我来帮你分析一下……”“这个问题问得很好”等客套铺垫，结尾是“希望以上内容对你有帮助”“如果还有疑问欢迎随时提问”“Hope this helps”等客套结束语，真正信息只占中间一小段。 | `filler_opening`、`filler_closing`（其中 1 条为英文样本）。 |
| `over_fragmentation` | block / material | 一个简单结论或操作被拆成 6–10 个小标题（或要点），每个小标题下只有一句短句，含“明确目标”“完成”“总结”等凑数小节；语义上属于过度拆分，规则层只能给出结构风险信号。 | `structure_anomaly`（info/minor 风险信号，不单独触发拦截）。 |
| `redundant_explanation` | block / material | 用户问的是具体结论、数字或字段，回答用大段篇幅复述该领域入门知识（索引是什么、pprof 是什么、所有权是什么等），真正答案被压到最后；属于语义冗余。 | 无（`[]`），由 Critic 语义判定，不依赖规则信号。 |
| `good_long` | pass / none | 600–1500 字符的正常长篇技术回答：像资深工程师的答复，有具体命令、字段、数字、取舍与验证方法；部分样本包含 fenced code block、表格或列表；无套话、无重复、无元评论。 | 无（`[]`）。 |
| `good_short` | pass / none | 1–3 句的简短合格回答：直接给出命令、API 或结论，无铺垫。自然语言不足 120 字符，按 balanced 策略不会调用 Critic。 | 无（`[]`）。 |

每个问题样本只突出**一个主要缺陷**：`duplicate_conclusion` 不叠加 filler，`filler_opening` 不叠加重复结论，`over_fragmentation` 不叠加套话，`redundant_explanation` 不叠加结构拆分，从而保证评测结果可解释。

## 5. 长度与内容约束

- 问题样本 `answer` 长度 60–600 字符；`over_fragmentation` 允许略长，当前基线最长 329 字符。
- `good_long` 长度 600–1500 字符；当前基线 631–875 字符。`good_short` 为 1–3 句，不设固定字数。
- “字符”按 Unicode rune 计（Go `utf8.RuneCountInString`）。样本以中文为主，可含英文技术名词与代码。
- 正常样本必须 `expected=pass`、`expected_severity=none`、`expected_rule_signals=[]`。规则层允许对正常样本输出 `info`/`minor` 的结构或长度风险信号（例如表格、短文本），这些信号不参与拦截判定。
- 问题样本的 `expected_severity=material`，表示需要 Critic 确认后拦截；确定性规则 finding 本身**永不**产生 BLOCK。

## 6. `expected_rule_signals` 语义

- 问题样本：列出的 finding 类型应至少命中一个，用于统计规则覆盖率（规则命中样本数 / 问题样本数）。
- 正常样本：必须为 `[]`。评测时以“规则层 material finding 数为 0”作为误杀判据，`info`/`minor` 不算误杀。
- 该字段描述的是**确定性规则的期望信号**，不描述 Critic 输出；Critic 判定以 `expected` 与 `expected_severity` 为准。

## 7. 如何新增样本

1. 复制现有行结构，追加一行完整 JSON 对象。不要修改已有 id（评测基线需要稳定），新 id 递增且全局唯一。
2. 新增问题样本：先确定唯一主缺陷类别，再写 `notes` 说明预期；`answer` 长度按第 5 节控制。
3. 新增 `duplicate_conclusion` 样本：必须包含一句 ≥30 字、逐字重复于不同段落的长句（不能放在 fenced code block 内），并把 `duplicate_sentence` 写进 `expected_rule_signals`。
4. 新增 `filler_opening` 样本：必须同时包含开头客套与结尾客套，并在 `expected_rule_signals` 写明 `filler_opening` / `filler_closing`。
5. 新增 `over_fragmentation` 样本：使用 6–10 个小标题或要点，每个下面只有一句短句；`expected_rule_signals` 写 `structure_anomaly`，或在规则层尚未实现时留空。
6. 新增正常样本：不得含套话、重复句、元评论（如“希望有帮助”“以上是我的回答”）；`expected_rule_signals` 必须为空。
7. 更新本 README 第 1 节的计数表；若新增类别，还要同步 `docs/design.md` 第 12.1 节以及规则/评测代码。
8. 提交前跑 `go test ./...`。checker 回归测试会读取本目录；目录缺失时测试应优雅跳过。

## 8. 校验片段（只读，不写入仓库）

下面片段用于快速检查行数、类别计数和 JSON 可解析性；字符长度以 Go 的 `utf8.RuneCountInString` 为准（CJK 在 .NET 中为单个 UTF-16 code unit，与 rune 数一致，含 emoji 时以 Go 结果为准）。

```powershell
$pos = Get-Content testdata/cases/positive.jsonl | ForEach-Object { $_ | ConvertFrom-Json }
$neg = Get-Content testdata/cases/negative.jsonl | ForEach-Object { $_ | ConvertFrom-Json }
"positive=$($pos.Count) negative=$($neg.Count)"
$pos | Group-Object category | Select-Object Name, Count
$neg | Group-Object category | Select-Object Name, Count
# 长度粗检
$pos | Where-Object { $_.category -ne 'over_fragmentation' -and ($_.answer.Length -lt 60 -or $_.answer.Length -gt 600) }
$neg | Where-Object { $_.category -eq 'good_long' -and ($_.answer.Length -lt 600 -or $_.answer.Length -gt 1500) }
# id 唯一性
($pos + $neg) | Group-Object id | Where-Object Count -gt 1
```

## 9. 使用方

- `internal/checker` 的回归测试：统计规则覆盖率与正常样本误杀率。
- Critic 测试 / 评测脚本：用 mock server 验证协议、fail-open 与判定一致性（见 `docs/eval-report.md`）。
- 任何对样本的破坏性修改都会让历史评测数据不可比，请只追加、不重排、不改已有 id。
