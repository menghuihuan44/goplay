# 指标与验收

“功能存在”不等于“功能实现”。GoPlay 为每类能力定义可执行、可回归的指标；评测器 @cmd/goplay-eval@ 在固定 seed 的 mock 环境里跑完整链路，安全审计由 @cmd/goplay-audit@ 单独执行。

## 指标总表

| 能力 | 指标 | 定义 | 阈值 | 当前结果 |
|------|------|------|------|----------|
| 安全边界 | 禁用 API 源码发现 | 静态扫描注入/读内存/钩子符号 | 0 | 0（129 文件） |
| 安全边界 | PE critical 导入 | 解析 PE 导入表，critical 符号 | 0 | 0 / 12 PE |
| 感知 | 干净帧字段准确率 | 与 GroundTruth 逐字段比较 | ≥99% | 100% |
| 感知 | 0.1% 像素噪声准确率 | 随机黑白噪点 | ≥97% | 99.32% |
| 感知 | 0.5% 像素噪声准确率 | 随机黑白噪点 | ≥90% | 98.05% |
| 感知 | 1% 像素噪声准确率 | 随机黑白噪点 | ≥85% | 95.52% |
| 感知 | HP/敌血/房间/药水 @1% | 分字段准确率 | HP≥90%，其余≥80–95% | 99.90% / 98.23% / 98.02% / 85.94% |
| 从零学习 | 达到 90% 进度所需局数 | 同一 seed 连续游玩，首次 ≥90% 的局号 | ≤4 | 平均 1 |
| 从零学习 | 胜率 | 全部 episode 胜/总数 | ≥50% | 24/24 = 100% |
| 从零学习 | 引擎错误 | error 状态/执行异常 | 0 | 0 |
| 决策 | 平均/P95 决策延迟 | @DecisionLatencyMS@ 直方图 | P95 ≤ 250ms（mock/规则）；LLM 另计 | 面板可见 |
| 决策 | 回退率 | @policy_fallback_total / decisions@ | 观察项，不设硬阈值 | 真实 local 跑 0 回退 |
| 决策 | 事件边界介入频率 | 每次 Boundary 处理的消息数 | 观察项 | 面板/事件流 |
| 记忆 | 固定预算 | 256KB 下实际字节数 | ≤预算 | 262,078 / 262,144 |
| 记忆 | 压缩/淘汰发生 | compaction/eviction 计数 | ≥1 | 446 / 446 |
| 记忆 | 关键信息 Recall@K | 人工标注 must-remember 的 token 命中 Top-K | @1≥90%，@3/@5≥95% | 全 100% |
| 接管 | 接管延迟 | 人工输入到 @human_control@ | ≤1000ms | 9ms |
| 接管 | 接管期间动作泄漏 | 人工控制期间执行动作数 | 0 | 0 |
| 接管 | 恢复延迟 | 释放到恢复动作 | ≤2000ms | 120ms |
| 对话 | 约束生效延迟 | message 到 Hard 约束 | ≤1000ms | 10ms |
| 对话 | 约束后违规动作 | 被禁动作执行数 | 0 | 0 |
| 对话 | 约束后继续游玩 | 后续动作数 | ≥3 | 5 |
| 对话 | 偏好转软权重延迟 | message 到 SoftWeights | ≤1000ms | 11ms |
| 对话 | 偏好强度 | risk_aversion 权重 | ≥0.2 | 0.25 |
| 对话 | 偏好不打断 | 偏好应用后仍在执行 | 1 | 1 |
| 并发 | 实例隔离 | 共享 game/memory/ID 计数 | 0 | 0（2/4 实例） |
| 并发 | 人工接管串扰 | 一个实例接管影响另一个 | 0 | 0 |
| 本地模型 | 首 token 延迟 | SSE 第一个 delta | low 档 ≤400ms | 预热 48ms；冷启动 3458ms |
| 本地模型 | 解码速度 | delta tokens / 秒 | low 档 ≥25 | 63.9（真实）/ 94.3（mock） |
| 本地模型 | 链路完整性 | SSE delta 数、指标写入 | 全解析 | 8/8，TTFT/TPS 已写入 |
| 悬浮界面 | 可交互 | 面板/灵动岛可打开、可对话、可暂停 | 手工验收 | headless 截图 + API 测试 |
| Android | 客户端逻辑 | JVM 测试 | 全部通过 | 30 项 |

> 上表是 quick 评测 + 真实本地模型手工验证的组合结果。完整阈值表与原始数据见 [reports/eval-report.md](reports/eval-report.md) 和 [reports/eval-report.json](reports/eval-report.json)。

## 运行评测

```powershell
# 快速（约 18–25 秒）
go run ./cmd/goplay-eval -quick -json -report-dir reports

# 全量（更长，更多 seed/步数）
go run ./cmd/goplay-eval -json -report-dir reports

# 只跑 Go 测试层的评测
go test ./internal/eval -count=1 -v
```

评测套件：

1. **perception**：8 个 seed × 120 步 × 4 档噪声，逐字段与 @mockgame.GroundTruth@ 比较。
2. **learning**：每个 seed 复用同一个 Policy/Memory 连续 8 局，统计首次 90% 的局号、胜率、错误；证明“不是每局重置后从零”。
3. **memory**：固定 256KB；场景 A 灌入 900 条干扰 fact + 40 条高重要度 must fact + 240 条 event；场景 B 用 128 条 event 交错 must 事件触发压缩。测量 Recall@1/3/5。
4. **control**：MockHuman 触摸/释放测接管；@Submit@ 约束与偏好测边界生效；检查动作冻结与继续游玩。
5. **concurrency**：2/4 实例并行，检查实例 ID、Game、Memory、Lua 不共享，接管无串扰。
6. **localmodel**：httptest SSE 服务器 + 生产 @llm.OpenAI.CompleteStream@ 解析器，测 TTFT/TPS/指标链路；不下载真实模型。

## 指标实现位置

- @internal/metrics@：计数器/仪表/直方图，Prometheus 文本在 @/metrics@；
- @internal/eval@：评测套件、阈值检查、JSON/Markdown 报告；
- @internal/engine@：决策/动作/感知/接管延迟打点，episode 统计；
- @internal/policy@：@PolicyStats@（decisions、llm_calls、llm_fails、fallbacks、last_llm_error）；
- @internal/memory@：@Stats@（added、compactions、evictions、recalls、bytes、working/summaries/facts）；
- @internal/localmodel/bench.go@：TTFT/TPS 写入同一 registry。

## 如何解读

- 感知与学习数据来自 mock 渲染器，证明算法在**已知可测环境**中成立；真实游戏需要新的 profile/视觉模型，不能把 95.52% 直接当商业游戏精度。
- 记忆 Recall 是“must token 是否出现在 Top-K”的严格命中，不是语义相似度；它适合回归，但偏保守。
- 冷启动 TTFT 包含 llama.cpp 加载模型的时间，不能与预热 TTFT 混为一谈；两者都记录。
- 接管延迟是软件观测值（输入事件到状态切换），不包含显示器/输入设备本身延迟。
- 真实 LLM 决策会受网络/模型波动影响；评测的 localmodel 套件用 mock SSE 固定时序，只验证链路与指标，不验证模型质量。

## 验收命令汇总

```powershell
go build ./...
go test ./... -count=1
go test -race ./internal/engine ./internal/api ./internal/memory ./internal/scheduler -count=1
go run ./cmd/goplay-eval -quick -json -report-dir reports
go run ./cmd/goplay-audit -json -out reports/audit-report.json
powershell -NoProfile -ExecutionPolicy Bypass -File native/build.ps1   # 需要 MSYS2 UCRT64
```
