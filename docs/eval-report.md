# Crisp 规则层评测报告

- 日期：2026-11-02
- 代码版本：`941fec3`（checker 模块提交）
- 环境：Windows / Go 1.26.4 / amd64
- 样本：[testdata/cases/](../testdata/cases/)（20 条问题回答 + 10 条正常回答，共 30 条）
- 复现：`go test ./cmd/crisp -run TestRuleEvaluation -count=1 -v`

## 1. 评测方法

第一层只评测确定性规则（不调用大模型）：对每条样本执行一次 `checker.Analyze`，统计

- `with_signal`：至少命中一条规则发现的样本数；
- `material`：至少命中一条 material 级别发现的样本数（规则本身从不产生 BLOCK，material 只是交给 Critic 确认的信号）；
- `critic_routed`：在默认 balanced 策略下会送语义审查的样本数；
- 误杀：正常回答中出现的规则发现 / material 发现数量。

## 2. 结果总表

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

## 3. 核心指标

| 指标 | 结果 | 说明 |
| --- | --- | --- |
| 规则信号覆盖率（全部问题样本） | 15/20 = 75% | 剩余 5 条是纯语义类别 redundant_explanation，按设计不由规则覆盖 |
| 规则信号覆盖率（规则目标类别） | 15/15 = 100% | duplicate_conclusion 5/5、filler_opening 5/5、over_fragmentation 5/5 |
| 规则 material 命中率 | 10/20 = 50% | duplicate_conclusion 5/5、filler_opening 5/5；structure 信号为 minor，语义类别无信号 |
| 规则误杀率（正常样本 material） | 0/10 = 0% | 10 条正常样本零规则发现 |
| 规则误杀率（正常样本任意发现） | 0/10 = 0% | 没有噪声发现，balanced 下正常长篇才送 Critic |
| 送 Critic 比例（问题样本） | 20/20 = 100% | balanced 模式下全部问题样本都会得到语义审查 |
| 规则平均耗时 | 207.6 µs/条 | 同一进程内 30 条样本，最大 551.5 µs |

**结论**：规则层负责发现"可机械识别"的重复与填充语，零误杀；过度拆分/无效解释
这类纯语义问题按设计留给 Critic，因此 balanced 模式必须启用 Critic 才能覆盖全部六类问题。
economy 模式只送有规则发现的样本，会漏掉 redundant_explanation（本报告末节）。

## 4. Hook 集成测试

全部为自动化测试，覆盖设计 §12.4 要求：

| 场景 | 预期 | 结果 |
| --- | --- | --- |
| 正常回答 | `{}` | PASS |
| 第一次发现 material 问题 | block + 固定重写指令 | PASS |
| 重写后的第二次 Stop（stop_hook_active=true） | `{}` | PASS |
| stop_hook_active 缺失 | 放行并告警 | PASS |
| Hook 输入解析失败 | 放行并记录异常 | PASS |
| last_assistant_message 缺失/为空 | 放行 | PASS |
| 短回答（< 120 natural runes 且无规则发现） | 不调用 Critic，放行 | PASS |
| Critic 超时 | 放行，fail_open=true | PASS |
| Critic 返回非法 JSON | 放行，fail_open=true | PASS |
| Critic 返回未知枚举 | 放行，fail_open=true | PASS |
| Critic HTTP 500 | 放行，fail_open=true | PASS |
| Critic 未配置 | 放行，fail_open=true | PASS |
| 回答全文/API Key 写入 stderr | 不应出现 | PASS（日志脱敏测试） |
| stdout 任意场景 | 永远一个合法 JSON | PASS |

复现：`go test ./cmd/crisp -count=1 -v`；Hook 协议单元测试：`go test ./internal/hook -count=1`。

## 5. Critic 协议验证（mock）

由于当前环境没有可用的 Critic 服务凭据，本轮用 hermetic mock（httptest，无真实网络）验证协议与失败路径：

- 请求：`POST {base_url}/chat/completions`、temperature 0、stream=false、系统/用户消息结构、Bearer 头有无、模型名；
- 响应：pass / minor / material block 解析正确；usage、latency 写入 Verdict；
- 异常：非法 JSON、未知 decision/severity、空 choices、HTTP 500、超时、未配置 → 返回错误 → FAIL-OPEN；
- 覆盖率：94.7% statements（`go test ./internal/critic -cover`）。

**未验证项**：真实模型对 30 条样本的判定一致性。真实 Critic 一致性与调参需要配置
`QUALITY_GATE_BASE_URL/API_KEY/MODEL` 后跑：

```powershell
go test ./cmd/crisp -run TestRuleEvaluation      # 规则层
# 真实 Critic：对每条样本用 check-text 跑 3 次，取多数，人工抽检
Get-Content testdata/cases/positive.jsonl | % { ($_ | ConvertFrom-Json).answer } | ForEach-Object {
  $_ | .\bin\crisp.exe check-text
}
```

## 6. 重写改善率与总 Token 成本（待真实会话）

- 重写改善率：需要真实 Codex 会话。方法：开启 Hook 跑 N 个真实任务，对触发 block 的回答收集
  原始回答与重写回答，人工抽检 3 人打分（是否移除缺陷且未丢失事实/数字/代码），
  改善率 = 重写后无 material 缺陷的比例。
- 总 Token 成本：T_total = T_original + T_critic + T_rewrite。Hook 只有 T_critic（日志
  `critic_tokens`），T_original/T_rewrite 必须从 Codex 会话用量或可观测数据取得。
- FAIL-OPEN 比例：正常运行时 ~0；本次测试中 5 种故障路径均为 fail-open 且无回答被错误拦截。

## 7. 调参建议

1. balanced 为默认模式；economy 会漏检纯语义类别（redundant_explanation，
   以及无明显重复句的语义重复），只建议在 Critic 成本敏感且可接受漏检时使用。
2. duplicate 相似度阈值（Jaccard 0.85）在 30 条样本上零误杀；引入更长技术文档样本后
   需复测，避免模板化正常回答被误判为重复。
3. filler 短语表集中在 `internal/checker/filler.go`，新增场景时只改这一处并补样本。
4. 规则 material 一律交 Critic 确认：即使规则命中，也不允许规则单独 BLOCK。
5. 下一轮优先补真实 Critic 一致性测试（3 次取多数），再根据误杀/漏检调整 prompt 与阈值。
