# GoPlay Mobile（Android 外部 AI 游玩客户端）

面向 GoPlay Go 核心的 Android 客户端：用 **MediaProjection** 截屏，把 JPEG 帧通过 WebSocket 发给核心；核心返回 JSON 动作，客户端用 **AccessibilityService** 合成点击/滑动/长按/返回/文本；悬浮球提供对话、暂停/继续/停止。全程不 root、不注入游戏进程、不读写游戏内存、不挂钩图形 API、不加载内核驱动。

包名：`com.goplay.mobile`。协议：[`PROTOCOL.md`](PROTOCOL.md)。

---

## 验证状态（重要）

本目录在 **没有 Android SDK 的 Windows 机器**上编写，**没有执行过 Gradle/Android 编译，也没有在真机或模拟器上安装运行**。因此：

- 不能把本目录描述为“已编译通过”或“已在设备验证”。
- 已完成的是：Kotlin 源码、资源、Manifest、Gradle KTS 配置，以及协议层的 JVM 单元测试。
- 协议层（`app/src/main/java/com/goplay/mobile/protocol/` 的 5 个文件 + 4 个测试类）曾用独立下载的 Kotlin 2.0.21 编译器、kotlinx-serialization 1.7.3、JUnit 4.13.2 在临时目录编译并运行：**30 个测试通过**（含 human 接管信号编解码）。这**不等于** Android 工程整体可编译。
- 其余 Android 代码只做了静态编写与人工复核，首次用 Android Studio 构建时仍可能出现编译/API 兼容问题。

首次构建前请按下面的“构建”步骤安装 JDK 17 与 Android SDK 35。

---

## 架构（文字图）

```text
┌──────────────────────────── Android 设备（无 root） ────────────────────────────┐
│                                                                                │
│  MainActivity (AppCompat + XML View)                                           │
│   ├─ 连接/画质/FPS/最大边/仅 Wi-Fi/自动重连设置                                │
│   ├─ 权限入口：悬浮窗、录屏、无障碍、通知                                       │
│   ├─ 会话/截屏/指标状态、日志、本地模型查询                                     │
│   └─ 投屏授权 launcher ───────────────┐                                        │
│                                       ▼                                        │
│  ScreenCaptureService (前台服务, mediaProjection)                              │
│   ├─ startForeground 通知（含“停止”）                                           │
│   ├─ MediaProjection + VirtualDisplay + ImageReader(RGBA_8888)                  │
│   ├─ FrameEncoder: 裁剪 rowPadding -> JPEG(quality)                            │
│   ├─ 目标 FPS 节流、暂停/断线丢帧、屏幕旋转重建显示                              │
│   └─ OverlayController: TYPE_APPLICATION_OVERLAY 悬浮球/药丸/展开面板            │
│         └─ 对话输入、暂停/继续、停止、状态灯                                     │
│                                                                                │
│  AppGraph（进程级单例，GoPlayApp 创建）                                         │
│   ├─ SettingsStore / DeviceInfo / NetworkMonitor / BatteryMonitor / Metrics             │
│   ├─ CoreClient (OkHttp WebSocket) ────────┐                                    │
│   ├─ CoreRest (OkHttp HTTP) ───────────────┼──► Go core                        │
│   └─ ActionRouter ──► GameAccessibilityService                                  │
│                          ├─ dispatchGesture(tap/swipe/long_press)              │
│                          ├─ GLOBAL_ACTION_BACK                                  │
│                          ├─ ACTION_SET_TEXT（仅已聚焦输入框）                     │
│                          └─ 只读 eventType -> WS human(true/false)              │
└────────────────────────────────────────────────────────────────────────────────┘
                                      │
                     WS: JPEG 帧（二进制）
                     WS: action / ack / ping / stats / chat
                     WS: human（用户接管 true/false，只有事件类型）
                     REST: pause / resume / message / local status/recommend
                                      ▼
                         Go core（默认 :8420，另行实现）
```

### 关键设计

- **帧走 WebSocket，控制走 REST**：WebSocket 低延迟传图与动作；REST 处理暂停/继续、聊天消息、本地模型状态。
- **只使用公开 Android API**：MediaProjection 需系统授权；手势只通过 AccessibilityService；悬浮窗通过 `TYPE_APPLICATION_OVERLAY`。
- **背压**：只有当 WebSocket ready 且 OkHttp 队列为空时才 `send` 新帧，否则丢帧并计数，避免慢网络堆积 JPEG 占满内存。
- **动作相关性**：每个 ack 带动作 `id`；动作执行结果只表示手势 API 完成，不保证游戏业务成功。
- **进程内共享**：`GoPlayApp.graph` 被 Activity/截屏服务/无障碍服务共享；截屏服务停止不影响无障碍服务启用状态，反之亦然。
- **只读事件类型检测用户接管**：无障碍服务的手势检测只检查 `AccessibilityEvent.eventType`，不读取事件文本或窗口内容；检测到真实用户触摸/点击/滚动/文本变化时通过 WS 发送 `human` 信号，让核心立即交还控制权。AI 自己 `dispatchGesture` 的执行窗口及结束后 400ms 会抑制该检测。文本动作仍通过无障碍 API 定位已聚焦控件（`ACTION_SET_TEXT`），不读取页面内容。

---

## 目录结构

```text
android/
├─ build.gradle.kts / settings.gradle.kts / gradle.properties
├─ gradlew / gradlew.bat / gradle/wrapper/*        # Gradle 8.10.2 wrapper
├─ PROTOCOL.md                                     # 协议契约
├─ README.md
└─ app/
   ├─ build.gradle.kts
   ├─ proguard-rules.pro
   └─ src/
      ├─ main/
      │  ├─ AndroidManifest.xml
      │  ├─ java/com/goplay/mobile/
      │  │  ├─ GoPlayApp.kt, AppGraph.kt, MainActivity.kt
      │  │  ├─ accessibility/GameAccessibilityService.kt, ActionRouter.kt
      │  │  ├─ capture/FrameEncoder.kt, ScreenCaptureService.kt
      │  │  ├─ data/AppSettings.kt, SettingsStore.kt, DeviceInfo.kt,
      │  │  │      NetworkMonitor.kt, BatteryMonitor.kt, Metrics.kt, LogBus.kt, SessionState.kt
      │  │  ├─ net/CoreClient.kt, CoreRest.kt
      │  │  ├─ notifications/Notifications.kt
      │  │  ├─ overlay/OverlayController.kt, OverlayPanel.kt
      │  │  └─ protocol/Models.kt, MessageCodec.kt, FrameCodec.kt,
      │  │         ActionMapper.kt, BackoffPolicy.kt
      │  └─ res/
      │     ├─ layout/activity_main.xml
      │     ├─ xml/accessibility_service_config.xml
      │     ├─ values/{strings,colors,themes}.xml
      │     └─ drawable/*.xml
      └─ test/java/com/goplay/mobile/protocol/
         MessageCodecTest.kt, FrameCodecTest.kt,
         BackoffPolicyTest.kt, ActionMapperTest.kt
```

---

## 权限与用户流程

Manifest 声明：

| 权限 / 配置 | 用途 | 用户操作 |
|---|---|---|
| `INTERNET` | WebSocket + REST | 安装时授予 |
| `ACCESS_NETWORK_STATE` | 网络/仅 Wi-Fi 判断 | 安装时授予 |
| `SYSTEM_ALERT_WINDOW` | 悬浮球/药丸 | 设置页“悬浮窗权限”→ 系统页打开 |
| `FOREGROUND_SERVICE` + `FOREGROUND_SERVICE_MEDIA_PROJECTION` | 截屏前台服务（Android 14+ 必需类型） | 安装时授予 |
| `POST_NOTIFICATIONS` | Android 13+ 前台通知可见 | 设置页“通知权限”请求，或系统通知设置 |
| MediaProjection | 截屏授权 | 点“开始截屏”后系统弹窗确认；每次会话需重新确认 |
| AccessibilityService（`BIND_ACCESSIBILITY_SERVICE` + 系统绑定） | 合成手势/返回/文本 | 设置页“无障碍服务”→ 系统无障碍页手动启用 |

首次使用建议顺序：

1. 填核心 host/port/实例 ID（默认 `192.168.1.100:8420`，实例 `android-1`），保存。
2. 授予悬浮窗权限（否则仍可仅用主界面，但无悬浮控制）。
3. 启用 GoPlay 无障碍服务（否则动作 ack 返回 `accessibility_unavailable`）。
4. Android 13+ 授予通知权限（否则前台服务通知可能不可见，服务仍运行）。
5. 点“开始截屏”→ 系统录屏弹窗确认。
6. 返回游戏，悬浮球/药丸可见；点击展开对话与暂停/继续/停止。

> 系统安全窗口、DRM 保护内容、部分厂商的“隐私屏/安全键盘”会让截屏黑屏或隐藏输入框；见“限制”。

---

## 构建

### 环境

- Android Studio（Ladybug 或更新）或命令行 + Android SDK。
- JDK 17（Gradle/AGP 8.7.x 要求）。
- Android SDK Platform 35、Build-Tools 35.x。
- 本工程使用 Gradle **8.10.2** wrapper；首次构建会自动下载 Gradle 发行包（网络需可访问 services.gradle.org 或配置镜像）。

### Android Studio

1. `File → Open` 选择 `C:\Codes\goplay\android`（不是仓库根目录）。
2. 等待 Gradle Sync。若提示 SDK 缺失，安装 Platform 35，并在 `android/local.properties` 写入：
   ```properties
   sdk.dir=C\:\\Users\\<you>\\AppData\\Local\\Android\\Sdk
   ```
3. 运行 `:app` 到 API 26+ 设备/模拟器。

### 命令行（Windows）

```bat
cd C:\Codes\goplay\android
gradlew.bat :app:assembleDebug
gradlew.bat :app:testDebugUnitTest
gradlew.bat :app:installDebug
```

Linux/macOS：

```bash
cd android
./gradlew :app:assembleDebug
./gradlew :app:testDebugUnitTest
```

产物：`app/build/outputs/apk/debug/app-debug.apk`。

### 测试

JVM 单元测试（无需设备）覆盖：

- `MessageCodecTest`：hello/ack/pong/human 编解码、包装/直接动作、包装层 id 继承（供 ack 关联）、缺失 action、未知字段、非法 JSON。
- `FrameCodecTest`：GPF1 往返、魔数/长度/空 JPEG/坏 header。
- `BackoffPolicyTest`：500→1000→…→30000 的无抖动序列、±20% 范围、封顶、负 attempt。
- `ActionMapperTest`：各动作映射、默认值/夹取、坐标与文本校验、不支持动作。

运行：`gradlew.bat :app:testDebugUnitTest`，报告在 `app/build/reports/tests/testDebugUnitTest/index.html`。

**已知验证结果**：协议层曾用独立 Kotlin 编译器在仓库外临时目录执行，`OK (30 tests)`，exit 0；Gradle 测试任务**尚未运行**（本机无 Android SDK/Gradle 安装，仅下载了 wrapper），因此不宣称 Gradle 测试通过。

---

## 与 Go 核心的时序

### 启动/截屏/动作

```text
用户                    MainActivity             ScreenCaptureService        Go core
 | 开始截屏                  |                           |                        |
 |-------------------------->| createScreenCaptureIntent |                        |
 | 系统授权                  |                           |                        |
 |<--------------------------|                           |                        |
 |                           | startForegroundService(授权 data)                  |
 |                           |-------------------------->|                        |
 |                           |                           | startForeground       |
 |                           |                           | getMediaProjection     |
 |                           |                           | VirtualDisplay/ImageReader
 |                           |                           | ensureConnected ------>| WS upgrade + hello
 |                           |                           |<----------------------| action / ping
 |                           |                           | JPEG 帧 -------------->|
 |                           |                           |<----------------------| ack
 |                           |                           | AccessibilityService 执行手势
 | 悬浮球暂停                 |                           |                        |
 |-------------------------->| REST pause -------------->| POST /api/control/pause
```

### 用户接管 / 恢复

```text
用户触摸游戏 -> AccessibilityService 只读 eventType
             -> WS human(active=true, reason=accessibility)（操作期间每 1s 心跳）
             -> 核心立即交还控制权（human_control）
用户停手约 2s -> WS human(active=false)
核心 2.5s 无新 human 信号 -> 恢复 AI
AI dispatchGesture 执行窗口 + 回调结束后 400ms -> 客户端抑制 human 检测
```

### 设置变更

保存设置后：

- 若截屏服务运行中，发送 `ACTION_RELOAD_SETTINGS`：更新画质/FPS/最大边；最大边变化会重建 VirtualDisplay。
- 若 WebSocket 处于连接/连接中/重连中，`reconnectIfActive()` 断开并用新地址重连（延迟 150 ms）。
- 仅 Wi-Fi 下载开关保存在 SharedPreferences，并通过 `X-GoPlay-Wifi-Only` 随 WebSocket/REST 请求发给核心；Android 自身不下载模型。

---

## 设置项

| 设置 | 默认 | 范围/说明 |
|---|---|---|
| host | `192.168.1.100` | 仅字母数字与 `._-`，自动去掉 `ws://`/`http://` 和路径 |
| port | `8420` | 1–65535 |
| 实例 ID | `android-1` | `[A-Za-z0-9._-]{1,64}`，必须与核心实例一致 |
| JPEG 质量 | 70 | 10–95 |
| 目标 FPS | 10 | 1–30 |
| 最大边 | 960 | 640/960/1280/1920（界面档位），存储夹在 320–3840 |
| 仅 Wi-Fi 下载 | 开 | 核心下载模型时使用；客户端网络监控提供 Wi-Fi/VPN/蜂窝状态 |
| 自动重连 | 开 | 关闭后断线即停止，不再后台重连 |

设置持久化在 `SharedPreferences("goplay_settings")`，不加密（不含密钥）。

---

## 本地模型档位（不在 APK 内）

- 设置页显示设备信息：Android 版本/SDK、厂商型号、ABI、总 RAM、CPU 核数。
- 点“查询核心本地模型”依次调用：
  - `GET /api/local/status`
  - `POST /api/local/recommend`，请求体含 `device`、`ramMb`、`abis`。
- 结果以原始 JSON 文本显示；核心端点暂未实现时会显示 HTTP 404/连接失败，不影响其他功能。
- **模型不打包进 APK**；首次下载由 Go 核心在电脑侧完成（例如通过核心 CLI/服务），Android 只展示推荐与状态。
- 下载是否走 Wi-Fi 由核心决定；客户端通过 `X-GoPlay-Wifi-Only` 传递用户意图，并展示网络状态用于诊断。核心尚未实现该头的强制逻辑时，开关不会阻止核心下载。

---

## 指标与观测

主界面“指标”区域与日志显示：

| 指标 | 来源 | 含义 |
|---|---|---|
| 截图 FPS | `Metrics.captureFps` / 服务每秒窗口统计 | 实际进入编码循环的帧率，非目标 FPS |
| 已捕获 | `Metrics.framesCaptured` | ImageReader 取到并成功编码的帧 |
| 已发送 / 丢弃 | `framesSent` / `framesDropped` | WebSocket 接受 / 因未连接、队列非空、编码失败丢弃 |
| 捕获字节 | `bytesCaptured` | JPEG 总字节，用于带宽估算 |
| 动作 / 错误 | `actionsExecuted` / `actionErrors` | 无障碍手势成功/失败次数 |
| 重连 | `wsReconnects` | WebSocket 断线重连尝试次数 |
| 动作延迟 | ack `executedMs` + 日志 | 从动作派发到手势回调完成；不含核心推理与网络 |
| 用户接管 | human 信号日志 / 核心状态 | 真实用户操作时发 true（1s 心跳），停手 2s 后发 false；不含按键内容 |
| 电量 | `BatteryMonitor` | 电池百分比与充电状态，显示在主界面与前台通知 |
| 网络 | `NetworkMonitor` | Wi-Fi/蜂窝/VPN/已验证，显示在主界面与前台通知 |
| 断线恢复 | 状态机 + 日志 | CONNECTING→CONNECTED→RECONNECTING；退避间隔可观察 |

限制：FPS 是客户端编码循环指标，不代表游戏渲染帧率；动作延迟不含核心决策时间；服务端 stats 的 `captureFps` 目前直接写入 UI。

---

## 限制与已知风险

1. **未编译/未真机验证**：如上，首次构建可能需要修复少量 API/资源问题；不要据此认为可直接发布。
2. **安全窗口/DRM**：银行、支付、部分游戏的安全窗口、DRM 视频、FLAG_SECURE 页面会返回黑屏或空白；这是 Android 平台保护，客户端不绕过，也不提供 root/注入方案。
3. **厂商限制**：部分 OEM 会杀后台、限制悬浮窗、限制无障碍、要求额外“后台弹出界面/自启动”权限；前台服务与通知已声明，但仍需用户手动放行。
4. **MediaProjection 授权**：每次开始截屏都要系统确认；旋转/分辨率变化会重建 VirtualDisplay；某些设备在切换应用后可能停止投影。Android 14+ 对 FGS 启动时机有严格要求。
5. **文本输入**：只对已聚焦的可编辑节点执行 `ACTION_SET_TEXT`，不会弹出/操作输入法；游戏自绘输入框或 WebView 非标准编辑器可能失败。
6. **无障碍能力**：手势可能被系统“无障碍超时”、多指操作、游戏反作弊/安全策略拒绝；`dispatchGesture` 一次只能有一个手势，客户端按顺序处理，不并行。
7. **网络与安全**：当前无 TLS、无认证、默认明文局域网；不要在公网暴露 8420。WebSocket 只支持文本控制 + 二进制帧；核心必须实现对应端点。
8. **模型 API 未实现**：`/api/local/status` 与 `/api/local/recommend` 是约定；核心实现前设置页会报错。
9. **性能**：高分辨率 + 高 FPS + 高质量 JPEG 可能造成发热/掉帧；默认 960 最大边、10 FPS、q70 是保守值。慢网络时丢帧优先于排队。
10. **进程死亡**：服务 `START_NOT_STICKY`；被系统杀死后需用户重新开始。无障碍服务由系统管理，客户端无法强制保活。
11. **日志隐私**：日志含主机、实例 ID、动作文本摘要；不包含游戏内存或账号密码。聊天内容会进入日志环（最多 200 行）。
12. **用户接管检测**：无障碍事件在部分游戏/厂商 ROM 上可能延迟或缺失，自绘 UI 的点击/滚动不一定产生 `TYPE_VIEW_*` 事件，因此以触摸交互事件为主，仍可能漏检。客户端发 true 后每 1s 心跳，停手约 2s 发 false；核心 2.5s 无新信号自动恢复。AI 注入手势会被自身抑制，但游戏侧仍可能把合成手势视作无障碍输入而拒绝。

---

## 安全边界（明确不做）

- 不注入游戏进程、不 hook、不 patch。
- 不读取游戏内存、不读取其他应用私有数据。
- 不挂钩 OpenGL/Vulkan/图形 API。
- 不加载未签名内核驱动、不请求 root。
- 截屏只通过用户逐次授权的 MediaProjection；手势只通过 Android 无障碍 API。

---

## 相关文档

- 协议细节、帧格式、REST、错误码、重连：[PROTOCOL.md](PROTOCOL.md)
- 源码入口：[`MainActivity.kt`](app/src/main/java/com/goplay/mobile/MainActivity.kt)、[`ScreenCaptureService.kt`](app/src/main/java/com/goplay/mobile/capture/ScreenCaptureService.kt)、[`GameAccessibilityService.kt`](app/src/main/java/com/goplay/mobile/accessibility/GameAccessibilityService.kt)
