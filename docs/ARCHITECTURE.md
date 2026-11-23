# 架构

## 设计目标

GoPlay 是一个**外部**游戏代理。它不进入目标进程，因此不能依赖读内存、注入 DLL 或图形 API 钩子。所有信息来自截屏/录屏，所有动作走操作系统公开输入通道（Windows @SendInput@、Android AccessibilityService）。这带来两个直接约束：

1. 感知层必须容忍像素噪声、遮挡和分辨率变化，输出带置信度的近似状态；
2. 游戏语义不能写死在 Go 里，否则每款游戏都要改核心。因此用 Lua profile 描述“状态如何解释、有哪些合法动作、动作产生什么奖励”。

## 分层

```text
cmd/goplayd ──> internal/api ──> internal/app ──> internal/engine
                                                   │
        ┌──────────────────────────────────────────┼───────────────────────┐
        │                    │                     │                       │
  perception.Source   luabridge.Profile      scheduler.Scheduler      policy.Policy
        │                    │                     │                       │
  Mock/Windows/Android   Lua profiles       buffer + adapter +      UCB + LLM + validator
        │                                        constraints
        └────────────> action.Executor <──────────────┘
```

| 包 | 职责 |
|----|------|
| @internal/domain@ | 跨层类型：Frame/State/Observation/Action/Constraint/Preference/Control |
| @internal/perception@ | @Source@ 抓帧；@Analyzer@ 输出 @domain.State@ 与 world |
| @internal/luabridge@ | 每实例独立 gopher-lua state；@observe/actions/interpret@ |
| @internal/mockgame@ | 可重复的像素级评测游戏 |
| @internal/policy@ | 上下文 bandit、LLM 混合、动作校验 |
| @internal/scheduler@ | 指令缓冲区、事件边界、两通道适配 |
| @internal/engine@ | 每实例 goroutine 与完整 tick |
| @internal/app@ | 实例构建、注册表、生命周期 |
| @internal/api@ | REST/WS、内嵌面板、Android 桥 |
| @internal/memory@ | 三层实例记忆 + 全局偏好记忆 |
| @internal/metrics@ | 计数器/仪表/直方图与 Prometheus 文本 |
| @internal/localmodel@ | 档位、下载、llama-server、bench |
| @internal/localruntime@ | 首次启动编排、provider 构造、状态 |

## 实例生命周期

@POST /api/instances@ 或 Android 首次连接会创建一个 @app.managed@：

1. 载入 Lua profile（@mock_dungeon@ / @generic@ / @generic_touch@）；
2. 创建感知源、分析器、执行器、人类检测器；
3. 创建独立 @Scheduler@、@Policy@ 和 @InstanceMemory@；
4. @engine.Runner.Start()@ 启动 goroutine。

状态机：@starting → running ↔ paused@，人工操作时 @human_control@，不可恢复错误 @error@，终局/停止 @stopped@。多个实例共享 bus、metrics、全局记忆和本地模型 provider，但决策状态完全隔离。

## 一个 tick

```text
Grab frame
  → Analyze (perception latency)
  → Profile.Observe → State/world
  → Profile.Actions → legal semantic actions
  → Scheduler.Boundary(BeforeDecision)
       drain instruction buffer:
         constraint → Control.Hard (deny actions/targets/tags)
         preference → global/instance preference → Adapt → SoftWeights/Hints
         command    → pause/resume/stop
  → terminal? record episode, maybe reset
  → Policy.Decide (hard filter → soft score → UCB/LLM)
  → Validator + rate limiter
  → Settle
  → Executor.Do
  → Grab + Analyze again
  → Profile.Interpret(before, after) → reward/terminal
  → Policy.Update + Memory.Add
```

事件边界是唯一允许对话改变控制的地方；正在执行的动作序列不会因为一条偏好消息被中途取消。约束在下一个边界成为硬条件。

## 两通道实现

### 实例约束（硬）

@Buffer.Submit@ 保存原始文本。@Boundary@ 调用 @RuleClassifier@ 分类，约束生成 @domain.Constraint@，支持 @DenyActions/DenyTargets/DenyTags@ 和 TTL。@policy.filter@ 在打分之前剔除被拒绝的动作；LLM 也只能看到过滤后的合法动作。

### 全局偏好（软）

偏好写入 @GlobalMemory.Preferences@ 或实例的 @InstancePrefs@。@Scheduler.RefreshControl@ 按 @AppliesTo@ 过滤后调用 @Adapt@：

- 若 @AvoidTags@ 与实例 @RequiredTags@ 冲突，标记 @suppressed@，不生效；
- 否则 map 到 @SoftWeights@（@aggression@、@risk_aversion@、@exploration@、@speed@、@resource_aversion@）；
- 只增加/减少候选分数，不删除候选，不生成 Constraint。

## 学习与决策

状态键：@phase|hp_bucket|room_bucket@；动作键：@kind|target@。每个键维护访问次数与平均奖励，UCB 分数：

```text
score = mean + c * sqrt(log(total+1) / (visits+1))
```

未见过的 arm 用先验分。LLM 每 @LLMEvery@ 次、强制请求或没有合法动作时参与；返回 JSON 经 @ExtractJSON@ 解析，再经 @groundLLMAction@ 映射到合法动作（精确 target → 同 kind 唯一 → 双向包含），避免小模型把“enemy 10/28”这类注释回显导致全部拒绝。仍非法才回退启发式。

求生先验只在 HP < 40% 时提高治疗/防御分数；这是通用安全启发式，不替代学习，也不删除动作。

## 并发模型

- 每实例一个 goroutine，tick 串行；
- @InstanceMemory@ 有互斥锁，持久化用锁内深拷贝；
- @GlobalMemory@ 有互斥锁；
- bus 使用带缓冲 channel 广播，WebSocket 写用 @wsSink@ 串行化；
- @metrics.Registry@ 用互斥保护；
- 关闭时先 cancel runner，再保存记忆、关闭 Lua/source/executor。

## 传输与前端

@internal/api@ 同时提供：

- REST：实例 CRUD、start/stop/pause/resume、message、constraints、memory recall、global preferences、metrics、local status；
- @/ws/instance/{id}@：快照/事件/对话/控制；Android 客户端用同一端点发 GPF1 二进制帧、收 action、回 ack；
- @/ws/events@：全局事件流；
- 内嵌 @index.html@（面板）与 @overlay.html@（药丸态灵动岛）。

Android 首次连接若实例不存在且带 @X-GoPlay-Client: android@，会按移动端默认值自动创建实例，避免手机端先手工建实例。

## 持久化

@<data>/instances.json@：实例注册表；@<data>/instances/<id>/memory.json@：实例记忆；@<data>/instances/<id>/control.json@：约束/实例偏好/聊天；@<data>/global.json@：全局偏好与抽象事实。所有写入使用临时文件 + rename。

## 扩展点

- 新游戏：写一个 Lua profile（@observe/actions/interpret@），或只用 @generic@ + 视觉 LLM；
- 新平台：实现 @perception.Source@ + @action.Executor@ + @HumanDetector@，在 @app.build@ 中注册；
- 新模型：在 @localmodel/catalog.go@ 增加带大小/SHA256/license 的 pin；
- 新指标：在 @metrics@ 常量 + eval suite 中加阈值。
