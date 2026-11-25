# GoPlay — 外部 AI 游戏代理

> 让 AI 自己学会玩，你随时可以接管。

GoPlay 是一个**外部运行**的 AI 自动游玩软件：它只通过截屏/录屏获取画面，通过系统输入或移动端无障碍服务执行动作，不注入游戏进程、不读写游戏内存、不挂钩图形 API、不加载内核驱动。核心用 Go，游戏语义用 Lua 描述，Windows 采集/输入/浮窗用 C++，Android 客户端用 Kotlin。

当前仓库是**可运行、可评测、可打包发布的 v0.6.1 实现**：

- 桌面 GUI：一个原生窗口（Windows WebView2 / macOS WebKit / Linux WebKitGTK），内嵌同一个面板，双击即用；也可退回系统浏览器。
- Windows：真实桌面截屏（GDI/DXGI）+ SendInput 输入 + 无边框置顶浮窗/托盘。
- Android（优先目标，v0.4「手机完全独立」）：Go 核心与 llama-server 以 arm64 原生库随 APK 安装，在本机子进程运行；Kotlin 壳只负责 WebView、MediaProjection 截屏、AccessibilityService 手势、系统悬浮球/灵动岛和连续语音。核心、模型下载、五页控制台（总览/游戏/AI 大脑/记忆/设置）都在手机上，不再依赖电脑。v0.4 增加权限中心、创建前准备流程、可操作的错误诊断卡、录屏中断后悬浮球保活、三档人工接管灵敏度、交还 AI、动态画面属性与实时预览；v0.4.1 修复进入游戏/旋转后重复报「旋转后重建截屏失败」的问题（改为复用同一个 MediaProjection 虚拟显示并调整 Surface，不再二次创建）。v0.4.2 修复启动时核心先于 WebSocket 连接就下发动作而报「no Android client connected」的竞态：客户端只在桥接就绪后开始，核心等待传输并在断线后不再复用旧帧。v0.4.3 针对游玩一段时间后断流：核心把断线/无帧视为「等待画面」而不是动作错误，WebSocket 每次收到帧或控制消息都重新绑定当前实例，录屏服务每 3 秒自动重连；通用触摸游戏连续两次等待/返回后强制点击或滑动，不再只空等。AI 大脑每层增加「自动」判断说明、运行时调用统计与已下载模型下拉，并在顶部直接回答「现在到底是什么在跑 / 哪些层尚未接入」；灵动岛显示 AI 是否在决策、最近动作/理由/进度，设置页增加持续实时通话入口（AI 说话时自动暂停麦克风，避免把自己的 TTS 当成用户指令）。
- 自动游玩：内置 mock_dungeon 像素级演示游戏，从零学习并在评测中达到 10 关通关；任何未知游戏可切换到通用 Lua profile + 视觉 LLM 描述。
- 本地模型：按设备 RAM/芯片三档推荐，模型权重不进安装包；可下载、启动、切换、删除多个模型，并按感知/分析/决策/对话/记忆/校验六层分别选择 API、本机模型或自动回退。Android APK 内置 arm64 llama-server 运行时与 Go 核心。
- Windows 游戏选择：新游戏弹窗会列出开始菜单与 Steam 已安装条目，选中后自动填窗口标题并可启动。实例必须绑定游戏窗口标题，只有该窗口在前台时才会截屏和 SendInput；切到聊天/浏览器会自动暂停，既不截屏也不注入。
- v0.6.0 有效动作与思维链：实例统计新增「有效动作」（本次动作带来 reward ≥ 0.05），不再只看动作数；游戏卡按 see/think/act/result/wait/resume/recover 展示 AI 看到的画面变化、选择依据、按下的键位坐标和结果，可直接判断 AI 是在有效分析还是空转。
- v0.6.0 通用学习修复：Lua 解释器此前把扁平 world map 当 values 读，导致真实游戏画面变化 44% 仍被记为 reward 0；修复后通用策略能对真实画面变化计分。连续成功的同一动作会加权重复（确认/射击），连续无回报才触发反循环；不再默认发送 Escape（未知游戏中可能退出）。真实 th06c.exe 上无任何游戏专用代码，从标题→难度→STAGE 1→弹幕战斗，40 秒 156 动作、93 个有效动作、reward 70.22、0 错误、0 误接管。
- v0.6.0 人类接管与输入隔离：Windows 人工检测改为轮询真实键鼠状态，不再被 DirectDraw 光标 warp 或 GetLastInputInfo 噪声误触发；AI 自身注入仍被过滤。Android 在灵动岛展开、前台是 GoPlay/输入法、前台包不是当前游戏时，AI 手势会让出输入；AI 手势期间浮窗设为不可触摸，避免 AI 自己点掉灵动岛。
- v0.6.0 多本地模型与语音层：本机可同时运行多个 llama-server，不同层可指定不同已下载模型，未运行层回退 API 或规则。实时语音作为独立层可在 AI 大脑选择系统识别/关闭、系统 TTS/API TTS/本地 TTS；灵动岛和设置页显示是否在听、是否在说、听到/说了什么。等待画面超过 3 秒会向手机请求重建截屏。
- v0.6.0 指令闭环：聊天中的「按 Z 键」「点开始」「往右走」会解析成具体动作，进入下一决策周期执行（20 秒有效），并在 trace 里显示 directive 来源；不再只回复「收到」。
- v0.6.1 语音与查看体验：语音桥接提升为进程级单例并配麦克风前台服务，切到游戏后仍保持连续识别；手机本机音色直接走系统离线 TTS，可选择已安装音色，不再误走核心的 OpenAI TTS 接口；API TTS 的模型/音色也改为选择式。点开灵动岛现在是“观察”，只让出触摸输入，截屏、分析与决策继续运行，面板能看到实时动作、理由、是否在听/说、听到/说了什么。人工接管新增灵动岛「我来操作/交还 AI 控制」和音量键，解决 SurfaceView 游戏收不到触摸事件、灵敏度无法切回接管的问题。
- v0.6.1 触摸端与知识：Android 实例强制使用 `generic_touch` 动作适配器，修复真机报 “keyboard action 'down' is not supported by the touch client”；触摸解释器修复 reward 恒 0。新游戏弹窗不再让用户选“游戏配置”，动作适配器按设备自动选择；”可用的游戏配置“改为说明它只是设备动作适配器，不含攻略。新建 Android 实例会把包名交给分析层，生成一段“游戏先验假设”（类型/目标/观察点）作为软提示，没有模型时明确从零学。每个决策周期仍只执行一个动作，动作序列尚未实现。
- v0.5.0 通用示范学习：人工接管时，AI 记录每次按键/点击前后的画面、画面变化和意图，并在相似画面复用同一动作；不包含任何东方/具体游戏专用代码。实测在真实 `th06c.exe` 上由外部脚本扮演人玩了 32 步，AI 随后在相似弹幕画面命中并执行示范动作 41 次（最近相似度 84.8%），206 个动作、0 错误、0 误接管。演示学习当前只在 Windows 端采集真实键鼠；Android 触摸示范尚未接入。
- v0.4.4 安全修复：AI 自己注入的键鼠不再触发人工接管（此前每个动作后都会误判，导致约 1.4–3.7 秒才能做一个动作）；目标窗口不在前台时实例进入等待，绝不把 w/a/s/d 打进当前聊天窗口；开局与每 10 个动作会生成语音自言自语（Android TTS / 桌面浏览器语音）。

## 目录

- [快速开始](#快速开始)
- [桌面 GUI](#桌面-gui)
- [一键打包发布](#一键打包发布)
- [八项需求与实现位置](#八项需求与实现位置)
- [架构](#架构)
- [两条控制通道](#两条控制通道用户约束-vs-跨游戏偏好)
- [记忆分层与压缩](#记忆分层与压缩)
- [本地模型](#本地模型)
- [感知与动作](#感知与动作)
- [指标与验收](#指标与验收)
- [Windows 原生组件](#windows-原生组件)
- [Android](#android)
- [项目结构](#项目结构)
- [已知边界](#已知边界)

## 快速开始

前置：Go 1.27.4（与 go.mod 一致）、Windows 10/11。Windows 原生浮窗需要 MSYS2 UCRT64 的 gcc/cmake；不构建原生组件也可以运行核心与 Web 面板。

```powershell
# 1. 构建并运行核心（离线 LLM，最稳的演示路径）
go build ./...
go run ./cmd/goplayd -addr 127.0.0.1:8420 -llm offline

# 2. 打开面板 / 灵动岛
#    http://127.0.0.1:8420/
#    http://127.0.0.1:8420/overlay

# 3. 另开终端：创建一个自动游玩的演示实例
go run ./cmd/goplay create -id demo -name "地牢试炼" -profile mock_dungeon -seed 7 -episodes 10
go run ./cmd/goplay watch -id demo
```

也可以完全用 HTTP 创建实例：

```powershell
Invoke-RestMethod -Method Post http://127.0.0.1:8420/api/instances -ContentType application/json -Body (@{
  id="demo"; name="地牢试炼"; profile_id="mock_dungeon"; seed=7; max_episodes=10; auto_start=$true
} | ConvertTo-Json)
```

一键冒烟脚本（会构建、启动临时 daemon、创建实例、等待结果并清理）：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File scripts/smoke.ps1
```

### 使用 API Key

```powershell
$env:GOPLAY_LLM_MODE = "api"
$env:OPENAI_API_KEY = "sk-..."
$env:GOPLAY_API_BASE = "https://api.openai.com/v1"
$env:GOPLAY_MODEL = "gpt-4o-mini"
go run ./cmd/goplayd
```

### 使用本地模型

```powershell
# 查看设备档位（不会下载）
go run ./cmd/goplay-model recommend

# 首次下载当前档位所需模型（可设置镜像）
$env:GOPLAY_HF_ENDPOINT = "https://hf-mirror.com"
go run ./cmd/goplay-model download --tier low

# 启动 daemon；local 模式会在 loopback 拉起 llama-server
go run ./cmd/goplayd -llm local
```

本地模式要求 llama-server 可执行文件：设置 local.runtime_path 或 GOPLAY_LLAMA_SERVER，或把它放入 PATH。详见 [docs/LOCAL_MODELS.md](docs/LOCAL_MODELS.md)。

## 桌面 GUI

```powershell
# 构建并启动桌面版（Windows 需要 WebView2 Runtime，Windows 11 通常已内置）
go run ./cmd/goplay-desktop

# 指定数据目录和端口；首次启动会在数据目录创建 config.json / desktop.log
go run ./cmd/goplay-desktop -data C:\GoPlayData -addr 127.0.0.1:8420

# 没有 WebView2 或想用浏览器时
go run ./cmd/goplay-desktop -browser
```

桌面壳与 daemon **同进程**启动：窗口加载内嵌面板，关闭窗口即停止实例并退出。面板右上角的“数据目录 / 退出”按钮通过 `goplayDesktop` 原生绑定调用；其余功能与 Web 版完全一致。窗口默认 1180×780，可用 `-width`/`-height` 调整。Linux 构建需要 `libgtk-3-dev` 与 `libwebkit2gtk-4.0-dev`；macOS 使用系统 WebKit。

## 一键打包发布

```powershell
# Windows：原生 GUI + 原生 DLL + CLI，生成 dist\GoPlay-<version>-windows-amd64.zip
go run ./cmd/goplay-release -target windows

# Android：生成 release/debug APK（需要 Android SDK）
go run ./cmd/goplay-release -target android -android-sdk C:\Android\sdk

# 当前主机的 GUI + Android + 交叉编译的 CLI 归档
go run ./cmd/goplay-release -target all -out dist

# 只交叉编译 Linux/macOS CLI（不需要本机 GUI 工具链）
go run ./cmd/goplay-release -target cli
```

每个产物都会写入 `SHA256SUMS.txt` 与 `release.json`。Android 签名读取 `GOPLAY_KEYSTORE`、`GOPLAY_KEYSTORE_PASSWORD`、`GOPLAY_KEY_ALIAS`、`GOPLAY_KEY_PASSWORD`；缺少时回退 debug 签名，保证 APK 可直接安装。GitHub Actions 工作流 `.github/workflows/release.yml` 在推送 `v*` 标签时构建 Windows/Linux/macOS/Android 四类产物并创建 Release。**模型权重不进安装包**，首次运行按设备档位下载；Android APK 内置 arm64 的 Go 核心与 llama-server 运行时，桌面版则使用外部 llama-server。

## 八项需求与实现位置

| # | 需求 | 实现 | 可检验证据 |
|---|------|------|-----------|
| 1 | 纯外部感知，不注入/不读内存/不挂钩 | internal/perception（GDI/DXGI、Android JPEG）、internal/native、internal/bridge、Lua profile | go run ./cmd/goplay-audit：源码 0 违规，PE critical 0 |
| 2 | 自动识别/操作/理解；人工操作立即交还 | internal/engine 主循环；action.SystemHuman / Android human 信号；human_resume_ms | 接管 9ms、冻结动作、释放后 120ms 恢复（reports/eval-report.md） |
| 3 | 多游戏并发、全局/实例记忆、偏好软权重适配 | internal/app（每实例独立 goroutine/Lua/记忆）、internal/memory、scheduler.Adapt | 2/4 实例隔离 0 串扰；冲突偏好 suppressed 且不生成硬条件 |
| 4 | 不中断游玩的语言/文字对话；指令缓冲区 | scheduler.Buffer + Boundary；约束走实例硬条件，偏好转软权重 | 约束 10ms 生效且 attack=0；偏好 11ms 生效且继续游玩 |
| 5 | API Key 与本地小模型；按档位下载；角色分工 | internal/llm（六层路由 + 多模型）、internal/localmodel、internal/localruntime | 三档目录 + 可选目录；模型下载/删除/切换 API 单测；真实 Qwen2.5-0.5B 决策 79 动作/1 胜/0 回退 |
| 6 | 灵动岛/悬浮窗 | 桌面原生 Win32 浮窗+托盘（GoPlay.exe 顶栏直接调起 goplay-overlay.exe）；Android 悬浮球/药丸 + 录屏中断保活 | 桌面 340×150 always-on-top 浮窗实测；Android 悬浮窗截图；断流后恢复入口 |
| 7 | 每条功能有指标 | internal/metrics、internal/eval、internal/audit | reports/eval-report.md：47/47 阈值 PASS；审计 PASS |
| 8 | Android 优先，其次 Windows | android/ 完整客户端（权限/诊断/语音/动态卡）；Windows 开始菜单/Steam 游戏选择 + 原生组件 | Android SDK 35 编译 v0.4 APK，30 项 JVM 测试通过；Windows ctest 2/2；列出本机 132 个开始菜单/Steam 条目 |

## 架构

```text
                       ┌────────────────────────────────────────────┐
                       │                goplayd                     │
                       │  HTTP / WebSocket / embedded Web UI        │
                       └───────────────┬────────────────────────────┘
                                       │
                 ┌─────────────────────┼─────────────────────┐
                 │                     │                     │
          ┌──────▼──────┐       ┌──────▼──────┐       ┌──────▼──────┐
          │ Instance A  │       │ Instance B  │       │ Instance C  │
          │ goroutine   │       │ goroutine   │       │ goroutine   │
          │ Lua state   │       │ Lua state   │       │ Lua state   │
          │ Memory 256K │       │ Memory 256K │       │ Memory 256K │
          └──────┬──────┘       └──────┬──────┘       └──────┬──────┘
                 │                     │                     │
   ┌─────────────┴─────────────────────┴─────────────────────┴──────────┐
   │  perceive → profile.observe → profile.actions → Boundary → policy  │
   │      ↑                                              ↓              │
   │  screenshot / JPEG                            input / gesture      │
   │      │                                              │              │
   │  Mock | Windows GDI/DXGI | Android MediaProjection  │              │
   └─────────────────────────────────────────────────────┴──────────────┘
                 │
        ┌────────▼────────┐        ┌──────────────────────┐
        │ Global Memory   │        │ Local model runtime  │
        │ cross-game only │        │ tiered GGUF + llama  │
        └─────────────────┘        └──────────────────────┘
```

一个实例一个决策循环：抓帧 → 分析状态 → Lua 生成合法语义动作 → 调度器事件边界处理对话/命令 → 策略选择动作 → 执行 → 再抓帧 → Lua 解释奖励 → 更新 bandit/记忆。实例之间不共享游戏、Lua state、策略或记忆，只共享显式的全局偏好与指标总线。

## 两条控制通道（用户约束 vs 跨游戏偏好）

这是本项目的核心裁决规则，代码在 internal/scheduler/adapter.go 与 internal/policy/policy.go：

1. **用户对话中的约束**（例如“先别打那个 Boss”）进入实例通道：
   - 先进入独立**指令/偏好缓冲区**，不在对话当刻改变动作；
   - 调度器在下一个**事件边界** drain；
   - 生成 domain.Constraint，作为**硬条件**进入策略控制器，被拒绝的动作不会进入候选集；
   - 回复明确说明“下一个决策周期生效”。

2. **跨游戏沉淀的偏好**（例如“你太激进了”）进入全局通道：
   - 写入全局偏好（或显式指定“这个游戏”时写入实例偏好）；
   - 必须经过**实例侧适配层** Adapt：若偏好与当前实例规则冲突（例如偏好要求避免 advance，而本游戏规则 required_tags 包含 advance），该偏好被 suppressed；
   - 不冲突时转成 SoftWeights / Hints，只影响打分，**永不**变成硬约束；
   - 不打断正在执行的动作序列，只影响后续选择。

两条通道在调度器处汇合，但优先级和生效方式不同：实例约束优先于全局偏好；全局偏好只能软注入。/api/instances/{id}/message 的 202 响应会说明进入缓冲区，面板聊天里也会显示分类结果。

关键词分类规则在 internal/scheduler/intent.go：支持 /constraint、/约束、/pref、/偏好 显式前缀；明确否定动作/目标会识别为约束；偏好词映射到 aggression、risk_aversion、exploration、speed、resource_aversion。

## 记忆分层与压缩

internal/memory 实现三层：

- **working**：最近的决策/观察，短期工作记忆；
- **summary**：每 8 条 work 折叠成确定性摘要，保留 kind 计数、tags、数值范围与高重要度文本；
- **fact**：长期事实，重要事实（importance ≥ 0.85）不会因预算淘汰。

固定预算默认 256KB。超预算时按 retention（重要度 + 新近度 + 访问次数 + 层级）淘汰低价值条目。召回使用 token 重叠 + 重要度 + retention 打分，并 bump 访问计数。

全局记忆 GlobalMemory 只接受 preference/style/safety/insight/fact，并且拒绝任何带 game_id 的具体游戏状态；实例记忆保存具体游戏状态。两者都做了并发保护，持久化走加锁快照，所以决策循环写入时保存不会 data race。

## 本地模型

- 模型与 llama.cpp 运行时**都不在安装包内**；首次启动根据 RAM/芯片推荐档位并下载。
- 档位目录：

| 档位 | 最低 RAM | 感知/过滤 | 策略推理 | 输出校验 | 目标 TTFT/TPS |
|------|---------|-----------|----------|----------|---------------|
| low | 8 GiB | 规则 | Qwen2.5-0.5B Q4_K_M | 规则 | ≤400ms / ≥25 |
| medium | 16 GiB | SmolLM2-360M 混合 | Qwen2.5-1.5B | 规则 | ≤600ms / ≥15 |
| high | 32 GiB | Qwen2.5-0.5B 混合 | Qwen2.5-3B | 规则 | ≤900ms / ≥8 |

分工是**流水线**而非投票集成：廉价模型/规则先过滤状态，推理级小模型只做策略，规则引擎做动作校验和序列化。模型文件带固定大小、SHA256、license；下载支持断点续传和镜像。

真实测试（本机 15.63GiB，low 档）：
- 模型 491,400,032B 下载并 SHA256 校验通过；
- 冷启动 TTFT 3458ms（超出预算，属冷加载），预热后 TTFT 48.4ms、TPS 63.9；
- 真实 llama.cpp 决策：79 个动作、1 局胜利、0 个 LLM 失败、0 次回退。

## 感知与动作

**感知源**：perception.Source
- MockSource：像素级渲染 + 抗噪分析，用于可重复评测；
- Windows native.ScreenSource：GDI 或 DXGI 抓桌面，返回 RGBA；
- Android bridge.Session：客户端 GPF1 二进制帧（JPEG），核心不接触游戏进程。

**分析器**：
- MockAnalyzer：血条竖向多点多数投票、房间号 3x5 模板、药水计数、死亡/胜利色块；
- VisionAnalyzer：通用兜底，调用视觉 LLM 生成状态描述；
- GenericAnalyzer：无模型时的亮度/颜色统计兜底。

**动作**：action.Executor
- Windows：SendInput 键盘/鼠标；
- Android：WebSocket 发 tap/swipe/long_press/back/text/wait，客户端无障碍服务执行并 ack；
- Mock：键盘语义动作；
- 所有动作经过 policy.Validator（类型、风险、归一化坐标）与 Limiter（默认 8 动作/秒）。

**从零学习**：policy 使用上下文 UCB bandit，状态键为 phase/HP 桶/房间桶，动作键为 kind+target；没有先验时探索，失败后更新均值。LLM 每 N 次或强制时参与，非法输出先 grounding 到合法动作，再失败才回退启发式。内置求生先验只在 HP < 40% 时提升治疗/防御分数，不删除合法动作。

## 指标与验收

```powershell
# 全量测试
go test ./... -count=1

# 竞态（核心路径）
go test -race ./internal/engine ./internal/api ./internal/memory ./internal/scheduler -count=1

# 评测：感知/学习/记忆/接管/并发/本地模型链路
go run ./cmd/goplay-eval -quick -json -report-dir reports

# 安全审计：源码 + PE 导入表
go run ./cmd/goplay-audit -json -out reports/audit-report.json
```

最近一次结果（详细表见 [reports/eval-report.md](reports/eval-report.md)）：

- 感知：干净帧 1.0000；1% 像素噪声 0.9552；HP 0.9990、敌血 0.9823、房间 0.9802、药水 0.8594。
- 学习：8 个 seed × 8 局，24/24 胜，达到 90% 进度的平均尝试次数 1，engine errors 0。
- 记忆：256KB 预算内 262,078B；Recall@1/3/5 全 1.0；压缩与淘汰均发生。
- 接管：9ms 接管、0 动作泄漏、120ms 恢复；约束 10ms 生效、attack=0；偏好 11ms 生效、继续游玩。
- 并发：2/4 实例 0 串扰、0 共享 ID。
- 本地模型链路：TTFT 0ms（mock SSE）、94.3 tok/s、8 个 delta、指标链路完整。
- 审计：129 个源码文件 0 违规，10 个 PE 0 critical；16 条 review 级导入来自 CRT/Go runtime，源码未调用，不计失败。报告扫描的是当前工作区，包含 `bin/` 与 `native/build/` 的构建产物。

> 这些指标证明“机制已实现且可回归”，不等价于在所有真实商业游戏上达到同样精度。真实游戏需要新的 Lua profile/视觉提示与更多数据。

## Windows 原生组件

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File native/build.ps1
```

产物在 native/build/：goplay_native.dll（截屏 + SendInput）与 goplay-overlay.exe（无边框 always-on-top 浮窗 + 托盘）。Go 通过纯 syscall 加载 DLL，不使用 cgo。详见 [native/README.md](native/README.md)。

创建 Windows 实例（需要已构建 DLL）。API/界面必须提供游戏窗口标题；只有该标题对应的窗口在前台时才会截屏和发送键鼠：

```json
{"id":"desktop","name":"Desktop","profile_id":"generic","source_kind":"windows","target_window":"Scarlet Devil"}
```

Windows 实例用 SystemHuman（GetLastInputInfo）检测人工操作，不使用任何钩子；AI 自己注入的输入通过 MarkInjected 排除，不会误判成人工接管。**游戏窗口不在前台时实例进入“等待画面”，既不截屏分析也不发送输入**，避免把按键打进聊天窗口或其它程序。当前 Windows 版因此不支持“用户继续用电脑、AI 在同一桌面后台玩”；后台窗口 PrintWindow 抓取对 DirectDraw 旧游戏实测只得到标题栏/空白，不能作为可靠感知源。要同时使用电脑，需要独立 Windows 会话/虚拟机，暂未实现。

## Android

v0.4 起 Android 继续「手机完全独立」：Go 核心（`libgoplaycore.so`）和 llama-server（`libllama-server.so`）随 APK 安装为 arm64 原生库，由 Kotlin 壳以独立子进程启动在 `127.0.0.1`；WebView 加载核心内置的五页控制台，不再依赖电脑。设置页内置权限中心（悬浮窗/无障碍/通知/录屏），新游戏默认先准备权限与画面再启动 AI。

- LocalCore 子进程 + `/health` 就绪探测，WebView 自动连本机核心；
- MediaProjection + ImageReader 抓屏并 JPEG 编码；
- AccessibilityService 执行手势；
- 系统级悬浮球 / 药丸 / 多行对话面板（暂停/面板/停止）；
- 已安装游戏选择、录屏/无障碍/悬浮窗/通知权限入口；
- 「AI 大脑」页填 API Key 或按设备档位下载本机模型，本机 llama-server 推理；
- WebSocket（GPF1 帧 + 动作 ack）与 REST 控制。

协议见 [android/PROTOCOL.md](android/PROTOCOL.md)，构建说明见 [android/README.md](android/README.md)。

> v0.4 已用 Android SDK 35 / build-tools 35.0.0 编译 release 与 debug APK，包含 arm64 的 Go 核心与 llama-server（release 约 13.1MB，debug 约 14.9MB；精确 SHA256 见 `dist/SHA256SUMS.txt`）。**尚未真机/模拟器验证**；未配置签名材料时回退 debug 签名，可直接安装内测但不能上架。

## 项目结构

```text
cmd/goplay-desktop 跨平台桌面壳（WebView2/WebKit/Cocoa）
cmd/goplay-release 一键 release 打包（Windows/Android/Unix/CLI）
cmd/goplayd        核心 daemon（REST/WS/Web UI）
cmd/goplay         控制 CLI
cmd/goplay-model   本地模型档位/下载/serve/bench
cmd/goplay-eval    评测入口
cmd/goplay-audit   安全审计入口
internal/service    daemon 生命周期（配置、监听、本地模型、实例管理）
internal/release    release 构建、归档、校验和
internal/engine    每实例决策循环
internal/app       实例管理与构建
internal/api       HTTP/WS/内嵌面板
internal/perception 截屏源与分析器
internal/policy    UCB + LLM 混合策略 + 校验
internal/scheduler 指令缓冲区、边界、适配层
internal/memory    三层记忆与全局记忆
internal/luabridge Lua profile 运行时
internal/localmodel 档位/下载/llama-server/bench
internal/localruntime 本地模型生命周期
internal/native    Windows DLL 绑定
internal/bridge    Android WebSocket 会话
internal/eval      可复现指标套件
internal/audit     静态与 PE 导入审计
native/            C++ 截屏/输入/浮窗
android/           Kotlin 客户端
docs/              架构、安全、指标、本地模型、Android
.github/workflows/ 四平台 release CI
reports/           评测与审计报告
```

## 已知边界

- 真实商业游戏的“从零学习”依赖通用视觉提示或新 Lua profile；仓库内的收敛数据来自内置 mock 游戏，不能直接外推。
- Windows 的 DXGI 在 HDR 桌面会回退 GDI；浮窗 exe 因 MinGW 运行时会导入 OpenProcess/SetThreadContext 等符号，但项目源码没有调用，审计将其列为 review 而非 critical。
- Android APK 已在本机编译，但尚未在真机/模拟器上验证 MediaProjection、无障碍手势、悬浮球、子进程和本机模型下载的端到端行为；本机 SDK 没有 emulator/system-images。
- v0.6 Android 只打包 arm64-v8a；`android/amd64` 需要 cgo+NDK，x86_64 模拟器不能运行。
- Windows 端已支持从开始菜单/Steam 选择并启动已安装游戏；实时语音入口目前只在 Android 壳实现，桌面端使用文字对话。
- 本地模型冷启动可能超过 TTFT 预算，推荐在 daemon 启动时预热；真实硬件上的 TPS 依档位而异。多模型并发启动与按层指定已在代码/API/UI 落地，但本机未下载真实权重做端到端多模型推理验证。
- 语音层已接通系统 TTS、OpenAI 兼容 `/audio/speech` 和 Android 原生 TTS/识别；未使用真实 API Key 或真机验证音质/延迟，桌面端 ASR 仍依赖浏览器 Web Speech。
- 示范学习是通用单帧相似度 + 单动作模仿，不理解游戏规则；动态画面同一场景实测约 0.83、不同画面约 0.59，阈值 0.80。它适合“人工示范一次、AI 在相似画面复用”，不是完整的长期技能规划。
- 东方 Project 只作为真实游戏测试台，仓库没有 `touhou` profile 或专用感知；测试使用 640x480 窗口，AI 只在游戏窗口前台时游玩。Touhou 标题闲置会进入 DEMO PLAY，此时按键不会开始新游戏；若已经停在 DEMO PLAY，重启游戏进程再让实例开始。
- 默认监听 loopback。若改为局域网监听，请自行加认证/防火墙，README 的威胁模型不覆盖公网暴露。

## 文档

- [docs/DESKTOP.md](docs/DESKTOP.md) — 桌面壳、WebView 依赖、启动/退出与排障
- [docs/RELEASE.md](docs/RELEASE.md) — 一键打包、CI、签名、产物清单与发布检查
- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — 模块、数据流、事件边界、并发模型
- [docs/SAFETY.md](docs/SAFETY.md) — 外部感知边界、审计规则、人类接管
- [docs/METRICS.md](docs/METRICS.md) — 每个功能的指标定义与阈值
- [docs/LOCAL_MODELS.md](docs/LOCAL_MODELS.md) — 档位、下载、运行时、性能
- [docs/ANDROID.md](docs/ANDROID.md) — 移动端架构与接入步骤
- [native/README.md](native/README.md) — Windows C++ 组件
- [android/README.md](android/README.md) — Android 工程