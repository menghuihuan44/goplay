# Android 接入

Android 是首要目标平台，但本仓库的 Go 核心运行在主机（PC/服务器/同一手机上的 Termux 均可）。手机端只负责“眼睛”和“手”：

- **眼睛**：MediaProjection + ImageReader 抓屏，JPEG 编码；
- **手**：AccessibilityService 执行 tap/swipe/long_press/back/text；
- **交互**：系统级悬浮球 / 药丸 / 展开面板；
- **传输**：WebSocket 把帧发给核心，接收动作并 ack。

核心不接触目标游戏进程，手机端也不 hook 游戏；录屏与无障碍都是系统公开能力。

## 数据流

```text
 Android app                          GoPlay core (PC / same device)
 ┌──────────────┐   GPF1 binary     ┌──────────────────────────┐
 │ MediaProjection│ ───────────────> │ bridge.Session.PushFrame │
 │ JPEG encoder   │                  │  → MockAnalyzer/Vision   │
 └──────────────┘                   │  → Lua → Policy          │
 ┌──────────────┐   action JSON     │                          │
 │ Accessibility │ <─────────────── │  bridge.Session.Do       │
 │ dispatchGesture│ ─── ack ───────> │                          │
 └──────────────┘                   └──────────────────────────┘
        ▲
        │ 系统悬浮球/药丸（TYPE_APPLICATION_OVERLAY）
```

## 快速连接

1. 主机启动核心：

```powershell
go run ./cmd/goplayd -addr 0.0.0.0:8420 -llm offline
```

只在可信局域网这样监听；公网请加认证/TLS。

2. Android 构建并安装 @android/@ 工程（Android Studio，授予录屏与无障碍权限）。默认服务器地址 @http://<host>:8420@，实例 @android-1@，WebSocket @ws://<host>:8420/ws/instance/android-1@。

3. 客户端首次连接会带 @X-GoPlay-Client: android@；核心若发现实例不存在，会自动创建 @source=android@、@profile=generic_touch@ 的实例并启动。无需手工建实例。

4. 打开悬浮球/药丸，可以对话、暂停/继续、查看血条/房间/当前动作。

## 协议

详见 [android/PROTOCOL.md](../android/PROTOCOL.md)。要点：

- 握手：WebSocket Upgrade + @X-GoPlay-Client: android@、@X-GoPlay-Protocol: 1@；客户端发 @{"type":"hello","protocol":1,...}@。
- 帧：二进制 @GPF1@，4B magic + 4B big-endian header length + JSON header + JPEG。header 含 @capturedAtMs/width/height/quality/rotation/sequence/instanceId@。单帧 JPEG ≤8MB，header ≤64KB。
- 动作：服务端发 @{"type":"action","id":"act-N","action":{"type":"tap","x":0.5,"y":0.5,...}}@；坐标归一化 [0,1]。客户端执行后回 @{"type":"ack","id":"act-N","ok":true,"actionType":"tap","executedMs":12}@。
- 人工接管：无障碍事件触发时发 @{"type":"human","active":true,"reason":"accessibility"}@；AI 手势窗口内抑制，空闲约 2s 后 @active:false@。
- REST 兼容：@POST /api/control/pause@、@POST /api/control/resume@、@POST /api/message@（@instanceId/text/kind@）。

## 设备档位

模型不进 APK。客户端可先问核心：

```http
POST /api/local/recommend
{"device":"Pixel 8","ramMb":7912,"abis":["arm64-v8a"]}
```

返回 @tier@、@approxBytes@、@downloadRequired@ 与角色计划。模型文件由核心（PC 或同机）下载与运行；手机只做感知/渲染/输入。若将来在手机本地跑 llama.cpp，可直接复用 @cmd/goplay-model@ 的档位与下载逻辑。

## 构建

当前环境没有 Android SDK，因此没有在本机编译 APK。构建步骤、权限、前台服务、悬浮窗限制见 [android/README.md](../android/README.md)。JVM 逻辑测试 30 项已通过（手工 Kotlin 编译执行，未跑 Gradle）。

需要重点验证的真机项：

- MediaProjection 在 Android 14+ 的前台服务类型与通知；
- 厂商 ROM 对 @TYPE_APPLICATION_OVERLAY@ 的额外授权；
- 无障碍手势在游戏全屏/横屏下的坐标旋转；
- 弱网下帧丢弃与重连退避；
- 电池优化白名单。

## 设计边界

- 手机端不读游戏内存、不注入、不 hook；
- 画面只在内存中保留最新一帧，服务端 1.5s 内复用，超时等待新帧；
- 断线时引擎阻塞等待重连，不把网络抖动当游戏终局；
- 人工触摸优先，AI 不抢输入。
