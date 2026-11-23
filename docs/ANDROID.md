# Android 接入

Android 是首要目标平台。v0.3 起采用「手机完全独立」形态：Go 核心和 llama-server 作为 arm64 原生库随 APK 安装，由 Kotlin 壳以独立子进程启动在 loopback；WebView 加载核心内置的五页控制台。手机端仍然只负责“眼睛”和“手”，不注入游戏、不读游戏内存、不 hook 图形 API。

- **眼睛**：MediaProjection + ImageReader 抓屏，JPEG 编码；
- **手**：AccessibilityService 执行 tap/swipe/long_press/back/text；
- **交互**：系统级悬浮球 / 药丸 / 展开面板，以及 WebView 控制台；
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

v0.3 只打包 `arm64-v8a`（覆盖真实手机）；`android/amd64` 需要 cgo+NDK，未打包，因此 x86_64 模拟器不能运行。模型权重不随 APK 打包。

## 首次使用

1. 打开 App：`LocalCore` 自动启动核心（随机 loopback 端口），WebView 自动加载控制台。
2. 「设置 → Android 权限」：悬浮窗 → 无障碍 → 通知。
3. 「游戏 → 新游戏」：从已安装游戏里选一个；创建时自动切换当前实例并请求录屏授权。
4. 授权后悬浮球/药丸出现；点它展开对话、暂停/继续/停止，「面板」回到控制台。
5. 「AI 大脑」：填 API Key，或按设备档位下载本机策略模型并启用本机 AI。

## 权限

| 权限/能力 | 用途 | 获取方式 |
|---|---|---|
| `FOREGROUND_SERVICE` + `FOREGROUND_SERVICE_MEDIA_PROJECTION` | 截屏前台服务 | 安装时授予 |
| `SYSTEM_ALERT_WINDOW` | 悬浮球/药丸/面板 | 设置页跳系统“显示在其他应用上层” |
| `POST_NOTIFICATIONS` | Android 13+ 前台通知可见 | 设置页请求，或系统通知设置 |
| `QUERY_ALL_PACKAGES` + `<queries>` | 列出已安装游戏供选择 | 安装时授予；上架前需说明用途 |
| MediaProjection | 截屏授权 | 系统弹窗，每次会话确认 |
| AccessibilityService | 合成手势/返回/文本 | 系统无障碍页手动启用 |
| `RECORD_AUDIO` | 语音走系统 `RecognizerIntent` | 由系统语音界面自行申请 |

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

- 已在 Windows 上构建：`:app:assembleDebug` 成功，debug APK 14,829,414B，SHA256 `BF66E61781C7C78139AC2669C6147C52746D4F6AE8337B39162213D20595F2C9`；包内含 arm64 的 `libgoplaycore.so` 与 `libllama-server.so`。
- **尚未真机/模拟器验证**：本机 SDK 无 emulator/system-images，也没有连接设备。MediaProjection、无障碍手势、悬浮球、子进程、模型下载都仍需设备验证。
- 未配置 release keystore 时回退 Android Debug 签名，只能本地安装，不能上架。
- 真机重点验证：Android 14+ 前台服务类型与通知；厂商 ROM 对悬浮窗的额外限制；横屏坐标旋转；弱网/后台保活；低 RAM 设备下载与推理延迟。

## 设计边界

- 手机端不读游戏内存、不注入、不 hook；
- 画面只在内存中保留最新一帧，服务端 1.5s 内复用，超时等待新帧；
- 只使用系统公开能力（MediaProjection / AccessibilityService / overlay）；
- 任何动作都经过规则引擎白名单与速率检查，再交给 AccessibilityService 执行。
