# Android 接入

Android 是首要目标平台。v0.4 继续采用「手机完全独立」形态：Go 核心和 llama-server 作为 arm64 原生库随 APK 安装，由 Kotlin 壳以独立子进程启动在 loopback；WebView 加载核心内置的五页控制台。手机端仍然只负责“眼睛”和“手”，不注入游戏、不读游戏内存、不 hook 图形 API。
v0.6.0 增加有效动作统计与 see/think/act/result 思维链、Windows 人工检测改轮询真实键鼠、等待画面自动请求重建截屏、多 llama-server 并发与按层指定模型、语音 ASR/TTS 独立配置；Android 手势在灵动岛展开、输入法/GoPlay 前台或前台包不是当前游戏时让出输入，AI 手势期间浮窗不可触摸。v0.6.2 在 APK 内加入 ML Kit 中文/拉丁离线 OCR：录屏帧约每 1.2 秒识别文字框并随帧头发给核心，触摸适配器可点击「开始游戏」「继续游戏」等真实标签并加权，不再只做 3×3 盲点；读到的文字进入游戏卡和思维链；Android 16 用 WindowInsets 避开状态栏/手势条。v0.6.1 把语音桥提升为进程级单例并由麦克风前台服务保活，切到游戏后连续识别继续；本机音色改为系统离线 TTS 音色选择，不再误走核心的 OpenAI TTS 接口；点开灵动岛只让出触摸输入，不暂停截屏/分析/决策，可实时查看 AI 状态；新增「我来操作/交还 AI」按钮与音量键显式接管；Android 实例强制 `generic_touch`，修复真机键盘动作错误和触摸 reward 恒 0；新建实例可生成游戏先验软提示（无模型则从零学）。v0.4 增加权限中心、创建前准备流程、结构化错误诊断、灵动岛保活、三档接管灵敏度与交还 AI、连续语音/TTS、多模型管理和六层 AI 路由。v0.4.1 修复旋转/进入游戏后重复报「旋转后重建截屏失败」：MediaProjection 只能创建一次 VirtualDisplay，现在复用同一个显示并调整尺寸与 Surface。v0.4.2 修复启动竞态：控制台把 WebSocket 桥接纳入就绪条件后再启动实例，核心动作最多等待 5 秒传输就绪，断线后不再复用旧帧。v0.4.4 增加语音自言自语；v0.5.0 加入通用示范学习核心（Android 触摸采集尚未接入）；v0.4.5 把语音改为麦克风前台服务（切到游戏后识别不再被 onPause 关闭），TTS 未就绪时排队而不是丢句，并优先中文语音；v0.4.3 修复游玩中长时断流：核心把断线/无帧归类为 transient，实例保持 running 并进入「等待画面」，恢复后继续而不是记成动作错误；WebSocket 在每帧与每条控制消息前重新绑定当前实例，录屏服务每 3 秒自动 ensureConnected；通用触摸 profile 连续两次 wait/back 后强制 tap/swipe。连续语音采用半双工：TTS 开始前暂停识别，播报结束或失败后自动恢复，避免把 AI 自己的声音识别成新指令。

- **眼睛**：MediaProjection + ImageReader 抓屏，JPEG 编码；
- **手**：AccessibilityService 执行 tap/swipe/long_press/back/text；
- **交互**：系统级悬浮球 / 药丸 / 展开面板、权限中心、诊断卡，以及 WebView 控制台；
- **语音**：SpeechRecognizer 连续识别 + TTS，不录音到磁盘；AI 播报期间暂停听音，播报结束自动恢复，防止回声自问自答；
- **大脑**：本机 Go core（规则/记忆/调度/策略）+ 按档位下载的 GGUF，由本机 llama-server 推理；
- **传输**：loopback WebSocket 把帧发给核心，接收动作并 ack。

## 进程与数据流

```text
 Android app (single APK)                    same device, loopback only
 ┌───────────────────────────────┐
 │ MainActivity (WebView)        │  HTTP 127.0.0.1:<port>
 │  └─ 五页控制台                │ ─────────────────────────┐
 │  └─ GoPlayHost JS bridge      │                          ▼
 │ LocalCore (ProcessBuilder)    │                 ┌──────────────────┐
 │  └─ libgoplaycore.so          │ ──────────────> │ Go core          │
 │                               │                 │ 感知/调度/记忆/  │
 │ ScreenCaptureService          │  WS frames      │ 策略/规则校验    │
 │  ├─ MediaProjection           │ ──────────────> │                  │
 │  ├─ JPEG encoder              │ <────────────── │ action JSON      │
 │  └─ OverlayController         │                 └────────┬─────────┘
 │ GameAccessibilityService      │                          │
 │  └─ dispatchGesture           │ <────────────────────────┘
 │                               │
 │ llama-server (子进程)         │  OpenAI /v1
 │  └─ libllama-server.so        │ <─────────────────────────
 └───────────────────────────────┘
```

核心只监听 `127.0.0.1`，局域网/公网都不可达；WebView 的 URL 也限定在这个来源。

## 构建

前置：JDK 17、Android SDK Platform 35、Build-Tools 35.0.0。Go 核心用 Android/arm64 交叉编译，不需要 NDK（`CGO_ENABLED=0`）：

```powershell
# 1. Go core -> jniLibs
$env:CGO_ENABLED="0"
$env:GOOS="android"
$env:GOARCH="arm64"
go build -buildmode=pie -ldflags "-s -w" `
  -o android/app/src/main/jniLibs/arm64-v8a/libgoplaycore.so ./cmd/goplayd

# 2. llama-server（已 strip）放进 jniLibs
copy .tmp-android-libs\libllama-server.so android\app\src\main\jniLibs\arm64-v8a\

# 3. APK
cd android
.\gradlew.bat :app:testDebugUnitTest :app:assembleDebug :app:assembleRelease
```

v0.4 只打包 `arm64-v8a`（覆盖真实手机）；`android/amd64` 需要 cgo+NDK，未打包，因此 x86_64 模拟器不能运行。模型权重不随 APK 打包。

推荐的一键构建（自动重建 Go 核心、布置 llama-server、产出 release APK）：

```powershell
$env:JAVA_HOME="C:\Program Files\Eclipse Adoptium\jdk-17.0.21.9-hotspot"
$env:ANDROID_HOME="C:\Android\sdk"
go run ./cmd/goplay-release -target android -out dist
```

## 首次使用

1. 打开 App：`LocalCore` 自动启动核心（随机 loopback 端口），WebView 自动加载控制台。
2. 「设置 → 运行准备 / Android 权限」：按顺序授予悬浮窗 → 无障碍 → 通知 → 麦克风 → 开始截屏；每一项实时显示状态。
3. 「游戏 → 新游戏」：从已安装游戏里选一个；创建后先准备权限和画面，准备好才启动 AI。
4. 授权后悬浮球/药丸出现；录屏中断时悬浮球保留并提供“恢复截屏”。点它展开对话、暂停/继续/停止，「面板」回到控制台。
5. 「AI 大脑」：填 API Key，或下载任意本机模型并启用；六层可分别选自动/API/本机/离线。

## 权限

| 权限/能力 | 用途 | 获取方式 |
|---|---|---|
| `FOREGROUND_SERVICE` + `FOREGROUND_SERVICE_MEDIA_PROJECTION` | 截屏前台服务 | 安装时授予 |
| `SYSTEM_ALERT_WINDOW` | 悬浮球/药丸/面板 | 设置页跳系统“显示在其他应用上层” |
| `POST_NOTIFICATIONS` | Android 13+ 前台通知可见 | 设置页请求，或系统通知设置 |
| `QUERY_ALL_PACKAGES` + `<queries>` | 列出已安装游戏供选择 | 安装时授予；上架前需说明用途 |
| MediaProjection | 截屏授权 | 系统弹窗，每次会话确认 |
| AccessibilityService | 合成手势/返回/文本 | 系统无障碍页手动启用 |
| `RECORD_AUDIO` | 连续语音识别（SpeechRecognizer） | 设置页权限中心请求；TTS 不需要 |

## 协议

详见 [android/PROTOCOL.md](../android/PROTOCOL.md)。要点：

- 握手：WebSocket Upgrade + `X-GoPlay-Client: android`、`X-GoPlay-Protocol: 1`；客户端发 `{"type":"hello","protocol":1,...}`。
- 帧：二进制 `GPF1`，4B magic + 4B big-endian header length + JSON header + JPEG。header 含 `capturedAtMs/width/height/quality/rotation/sequence/instanceId`。单帧 JPEG ≤8MB，header ≤64KB。
- 动作：服务端发 `{"type":"action","id":"act-N","action":{"type":"tap","x":0.5,"y":0.5,...}}`；坐标归一化 [0,1]。客户端执行后回 `{"type":"ack","id":"act-N","ok":true,"actionType":"tap","executedMs":12}`。
- 人工接管：无障碍事件触发时发 `{"type":"human","active":true,"reason":"accessibility"}`；AI 手势窗口内抑制，空闲约 2s 后 `active:false`。
- REST 兼容：`POST /api/control/pause`、`POST /api/control/resume`、`POST /api/message`（`instanceId/text/kind`）。

## 设备档位

模型不进 APK。控制台的「AI 大脑」页会显示当前设备档位、RAM、核心数和推荐模型；也可以直接问核心：

```http
POST /api/local/recommend
{"device":"Pixel 8","ramMb":7912,"abis":["arm64-v8a"]}
```

返回 `tier`、`approxBytes`、`downloadRequired` 与角色计划。模型由本机核心下载到 App 私有目录，由同 APK 内的 llama-server 加载；不经过电脑。

## 验证状态与限制

- 已在 Windows 上构建 v0.6.1：`:app:testDebugUnitTest`（30 个 JVM 测试）与 `:app:assembleDebug` 成功；release APK 约 13.1MB，包内含 arm64 的 `libgoplaycore.so` 与 `libllama-server.so`，versionCode 13 / versionName 0.6.2。精确大小与 SHA256 见 `dist/release.json`。
- **尚未真机/模拟器验证**：本机 SDK 无 emulator/system-images，也没有连接设备。MediaProjection、无障碍手势、悬浮球、子进程、模型下载都仍需设备验证。
- 未配置 release keystore 时回退 Android Debug 签名，只能本地安装，不能上架。
- 真机重点验证：Android 14+ 前台服务类型与通知；厂商 ROM 对悬浮窗的额外限制；横屏坐标旋转；弱网/后台保活；低 RAM 设备下载与推理延迟。

## 设计边界

- 手机端不读游戏内存、不注入、不 hook；
- 画面只在内存中保留最新一帧，服务端 1.5s 内复用，超时等待新帧；
- 只使用系统公开能力（MediaProjection / AccessibilityService / overlay）；
- 任何动作都经过规则引擎白名单与速率检查，再交给 AccessibilityService 执行。