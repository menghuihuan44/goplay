# GoPlay Mobile（Android 外部 AI 游玩客户端）

v0.4「手机完全独立」：Go 核心（`libgoplaycore.so`）和 llama-server（`libllama-server.so`）随 APK 安装为 arm64 原生库，由 Kotlin 壳以**独立子进程**启动在 `127.0.0.1`；Kotlin 壳负责 WebView 控制台、**MediaProjection** 截屏、**AccessibilityService** 手势、系统悬浮球/灵动岛、连续语音和权限中心。核心返回 JSON 动作，客户端合成点击/滑动/长按/返回/文本。全程不 root、不注入游戏进程、不读写游戏内存、不挂钩图形 API、不加载内核驱动。

v0.4 新增：设置页权限中心（悬浮窗/无障碍/通知/录屏逐项状态）、创建游戏前准备流程、错误的可操作诊断卡（code/检查项/建议/复制诊断）、录屏中断后悬浮球保活与恢复入口、三档人工接管灵敏度、交还 AI、动态属性卡与低帧率实时画面、连续语音识别 + TTS、多模型下载/切换/删除、六层 AI 路由。

v0.4.1 修复进入游戏/旋转后反复报「截屏已停止：旋转后重建屏幕失败」：MediaProjection 授权 token 只能创建一次 VirtualDisplay，旧代码在旋转时销毁并重建，必然失败。现在复用同一个 VirtualDisplay，只在 `onCapturedContentResize`/显示变化时调整尺寸和 Surface；调整失败也会保留旧画面继续运行，不再停掉截屏。

v0.4.2 修复启动竞态：旧版在录屏服务刚启动、WebSocket 尚未 attach 时就允许核心开始第一轮决策，核心会报 `execute 'wait|screen': bridge: no Android client connected`。现在控制台把「手机桥接」纳入就绪条件，准备好后 900ms 轮询确认 WS 已连才启动实例；核心侧动作最多等待 5 秒传输就绪，断线后的旧帧也不再复用。设置页可看到桥接状态并点「重新连接」。

v0.4.5 修语音持续性与真实游戏：进入游戏后不再因 Activity onPause 关闭麦克风，语音识别跑在 microphone 前台服务里；TTS 未就绪时排队播报、优先中文；东方 Project 新增 `touhou` profile 与像素分析器，菜单只按确认键并在游戏内持续射击/躲避。v0.4.4 增加开局/每 10 个动作的语音自言自语，并让桌面端只把键鼠发给绑定的游戏窗口；Android 本机继续复用 0.4.3 的断流恢复。v0.4.3 继续修游玩中长时断流：核心不再把「no Android client / no frame」当成动作错误，而是保持实例运行并显示「等待画面」，传输恢复后自动继续；WebSocket 每次收到帧都重新绑定当前实例；录屏服务每 3 秒自动重连。通用触摸 profile 连续两次等待/返回后强制点击或滑动，避免真实游戏里只空等。灵动岛现在显示 AI 状态/最近动作/理由/进度/错误；AI 大脑每层增加自动逻辑说明、运行时调用次数和已下载模型下拉；设置页增加持续实时通话入口。

包名：`com.goplay.mobile`。协议：[`PROTOCOL.md`](PROTOCOL.md)。

---

## 验证状态（重要）

本目录已在 Windows 上完成 v0.4 的命令行构建 + JVM 单元测试（2026-11 实测）：

- 环境：JDK 17.0.21 (Temurin)、Gradle 8.10.2（wrapper）、AGP 8.7.3、Kotlin 2.0.21、cmdline-tools 23.0、Platform 35、Build-Tools 35.0.0；SDK 位于仓库外的 `C:\Android\sdk`。
- 原生核心：`CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build -buildmode=pie -ldflags "-s -w"` 产出 `libgoplaycore.so`（v0.4 约 10.3MB）；llama-server 已 strip 为 `libllama-server.so`（13,174,768B）。两者放入 `app/src/main/jniLibs/arm64-v8a/`；`cmd/goplay-release -target android` 会自动重建/布置它们。
- 产物：`:app:assembleDebug` / `:app:assembleRelease` **BUILD SUCCESSFUL**；v0.4.5 release APK 约 13.1MB、debug APK 约 14.9MB，包内 `lib/arm64-v8a/` 两个原生库存在，`aapt` 显示 versionCode 9 / versionName 0.4.5 / native-code arm64-v8a。精确大小与 SHA256 以 `dist/release.json`、`dist/SHA256SUMS.txt` 为准。
- 签名：未提供 release 密钥时按设计回退 debug 签名（CN=Android Debug），可本地安装，**不可用于商店发布**。
- 测试：`:app:testDebugUnitTest` 的 30 个 JVM 测试全部通过；本目录未包含 MediaProjection/无障碍/悬浮窗/语音的 instrumentation 测试。
- **尚未在真机或模拟器上安装运行**：本机 SDK 没有 emulator/system-images，也没有连接设备。MediaProjection、AccessibilityService、悬浮球展开、本机核心子进程、模型下载仍需要设备验证。
- x86_64 模拟器未打包：Go 的 `android/amd64` 目标要求 cgo + NDK，而本机没有安装 NDK；v0.4 只包含 arm64-v8a，覆盖真实 Android 手机。

构建环境提示（AGP 8.7.3 与新版 cmdline-tools 的兼容性）：cmdline-tools 23.0 写出的 `package.xml` 是 SDK XML v4，而 AGP 8.7.3 只识别到 v3，会把已安装的 Platform 35 / Build-Tools 35 当作缺失并尝试重新下载。任选其一解决：安装较旧的 cmdline-tools（如 19.0）来装 SDK；或把这两个包的 `package.xml` 改写为 v3 可解析的格式（本次构建机采用后者，仅属机器本地修复，不在仓库内）。另外本机访问 dl.google.com / Maven Central 很慢，`android/settings.gradle.kts` 已把阿里云镜像排在官方仓库之前。

---

## 架构（文字图）

```text
┌──────────────────────────── Android 设备（无 root） ────────────────────────────┐
│                                                                                │
│  MainActivity (AppCompat + WebView)                                            │
│   ├─ 启动 LocalCore 子进程，等待 /health 后加载 http://127.0.0.1:<port>/       │
│   ├─ 同一个五页 Web 控制台（总览/游戏/AI 大脑/记忆/设置）                       │
│   ├─ GoPlayHost JS bridge：已安装游戏/切实例/截屏/悬浮球/语音/系统设置          │
│   └─ 投屏授权 launcher ───────────────┐                                        │
│                                       ▼                                        │
│  LocalCore (ProcessBuilder)                                                    │
│   └─ nativeLibraryDir/libgoplaycore.so -addr 127.0.0.1:<free> -data filesDir/core│
│        env: GOPLAY_LLAMA_SERVER=nativeLibraryDir/libllama-server.so            │
│                                                                                │
│  ScreenCaptureService (前台服务, mediaProjection)                              │
│   ├─ MediaProjection + VirtualDisplay + ImageReader(RGBA_8888)                  │
│   ├─ FrameEncoder: 裁剪 rowPadding -> JPEG(quality)                            │
│   ├─ 目标 FPS 节流、暂停/断线丢帧、旋转时复用同一投影调整尺寸/Surface            │
│   └─ OverlayController: TYPE_APPLICATION_OVERLAY 悬浮球/药丸/展开面板            │
│         ├─ 多行输入、发送、暂停/继续、面板、停止、状态灯                         │
│         └─ 「面板」回到 Web 控制台的设置页                                       │
│                                                                                │
│  AppGraph（进程级单例，GoPlayApp 创建）                                         │
│   ├─ SettingsStore / DeviceInfo / NetworkMonitor / BatteryMonitor / Metrics     │
│   ├─ LocalCore ────────────────────────────► 本机 Go core（子进程）             │
│   ├─ CoreClient (OkHttp WebSocket) ────────┘                                    │
│   ├─ CoreRest (OkHttp HTTP) ───────────────┘                                    │
│   └─ ActionRouter ──► GameAccessibilityService                                  │
│                          ├─ dispatchGesture(tap/swipe/long_press)              │
│                          ├─ GLOBAL_ACTION_BACK                                  │
│                          ├─ ACTION_SET_TEXT（仅已聚焦输入框）                     │
│                          └─ 只读 eventType -> WS human(true/false)              │
└────────────────────────────────────────────────────────────────────────────────┘
                                      │ loopback only
                      WS: JPEG 帧 / action / ack / human
                      REST: pause / resume / message / local status/download
                                      ▼
              Go core + llama-server（同一 APK，独立子进程，模型按档位下载）
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
      │  │  ├─ GoPlayApp.kt, AppGraph.kt, MainActivity.kt（WebView + GoPlayHost）
      │  │  ├─ accessibility/GameAccessibilityService.kt, ActionRouter.kt
      │  │  ├─ local/LocalCore.kt（Go 核心子进程）
      │  │  ├─ capture/FrameEncoder.kt, ScreenCaptureService.kt
      │  │  ├─ data/AppSettings.kt, SettingsStore.kt, DeviceInfo.kt,
      │  │  │      NetworkMonitor.kt, BatteryMonitor.kt, Metrics.kt, LogBus.kt, SessionState.kt
      │  │  ├─ net/CoreClient.kt, CoreRest.kt
      │  │  ├─ notifications/Notifications.kt
      │  │  ├─ overlay/OverlayController.kt, OverlayPanel.kt
      │  │  └─ protocol/Models.kt, MessageCodec.kt, FrameCodec.kt,
      │  │         ActionMapper.kt, BackoffPolicy.kt
      │  ├─ jniLibs/arm64-v8a/
      │  │  ├─ libgoplaycore.so        # Go 核心，AArch64 PIE
      │  │  └─ libllama-server.so      # llama.cpp 运行时，AArch64
      │  └─ res/
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
| `QUERY_ALL_PACKAGES` + `<queries>` | 列出用户已安装游戏，供“新游戏”选择 | 安装时授予；上架前需按商店政策说明用途 |
| MediaProjection | 截屏授权 | 点“开始截屏”后系统弹窗确认；每次会话需重新确认 |
| AccessibilityService（`BIND_ACCESSIBILITY_SERVICE` + 系统绑定） | 合成手势/返回/文本 | 设置页“无障碍服务”→ 系统无障碍页手动启用 |
| `RECORD_AUDIO` | 连续语音识别（SpeechRecognizer）需要 | 设置页权限中心请求麦克风权限；TTS 不需要 |

首次使用建议顺序：

1. 打开 App：`LocalCore` 自动启动本机 Go 核心（`127.0.0.1` 随机端口），WebView 自动加载控制台。
2. 在「设置 → 运行准备 / Android 权限」按顺序点：悬浮窗 → 无障碍 → 通知 → 麦克风（语音需要）→ 开始截屏。每项会实时显示已就绪/缺失。
3. 回到「游戏 → 新游戏」，从已安装游戏里选一个；创建后先走准备流程（缺权限会打开系统设置，授权后回到控制台继续），准备好才启动 AI；如勾选“创建后启动游戏”，授权后游戏会打开。
4. 授权后悬浮球/药丸出现；点它展开对话、暂停/继续/停止，点「面板」回到控制台。录屏中断时悬浮球保留并显示“恢复截屏”，不会一闪消失。
5. 在「AI 大脑」填 API Key，或下载任意本机模型并点「启用本机 AI」；六层可分别选自动/API/本机/离线。模型权重不随 APK 打包。
6. 回到游戏，AI 开始截屏/决策/手势。卡住时实例卡会显示结构化诊断和可执行建议；用户碰屏幕即接管，可调接管灵敏度或点「交还 AI」。

> 系统安全窗口、DRM 保护内容、部分厂商的“隐私屏/安全键盘”会让截屏黑屏或隐藏输入框；见“限制”。

---

## 构建

### 环境

- Android Studio（Ladybug 或更新）或命令行 + Android SDK。
- JDK 17（Gradle/AGP 8.7.x 要求）。
- Android SDK Platform 35、Build-Tools 35.x（本工程在 `app/build.gradle.kts` 固定 `buildToolsVersion = "35.0.0"`，避免 AGP 默认去下载 34.0.0）。
- 本工程使用 Gradle **8.10.2** wrapper；首次构建会自动下载 Gradle 发行包（网络需可访问 services.gradle.org 或配置镜像）。
- 已验证参考环境：Temurin JDK 17.0.21 + cmdline-tools 23.0 + Platform 35 + Build-Tools 35.0.0。

### Android Studio

1. `File → Open` 选择 `C:\Codes\goplay\android`（不是仓库根目录）。
2. 等待 Gradle Sync。若提示 SDK 缺失，安装 Platform 35 与 Build-Tools 35.0.0，并在 `android/local.properties` 写入（该文件已被 `.gitignore` 忽略）：
   ```properties
   # 示例：Windows 默认安装位置
   sdk.dir=C\:\\Users\\<you>\\AppData\\Local\\Android\\Sdk
   # 本次构建机：SDK 放在仓库外、路径更短
   # sdk.dir=C\:\\Android\\sdk
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

### 发布签名（Release）

`assembleRelease` 产出的 APK 必须签名才能安装。签名材料按以下顺序读取，**任何一项都不得提交到仓库**：

1. **环境变量**（推荐 CI/一次性构建）：

   | 变量 | 含义 |
   |---|---|
   | `GOPLAY_KEYSTORE` | keystore 路径，绝对路径或相对 `android/` 的路径 |
   | `GOPLAY_KEYSTORE_PASSWORD` | keystore 密码 |
   | `GOPLAY_KEY_ALIAS` | key alias |
   | `GOPLAY_KEY_PASSWORD` | key 密码 |

2. **`android/keystore.properties`**（已在 `android/.gitignore` 中忽略）：

   ```properties
   GOPLAY_KEYSTORE=C:\\keys\\goplay-release.jks
   GOPLAY_KEYSTORE_PASSWORD=***
   GOPLAY_KEY_ALIAS=goplay
   GOPLAY_KEY_PASSWORD=***
   ```

3. **Gradle 属性**：`-PGOPLAY_KEYSTORE=...` 或 `~/.gradle/gradle.properties`（与 `keystore.properties` 同名键）。

行为：

- 四项全部提供 → release 使用 `signingConfigs.release`，产物可用于分发。
- 四项全部为空 → **回退到 debug 签名**（`signingConfigs.debug`），APK 可直接 `adb install`，适合本机/内测安装；**不能用于商店上架**。
- 只提供一部分 → 构建直接失败并列出缺失项，避免“以为用了正式 key、实际用了 debug key”（实测：只给 `-PGOPLAY_KEYSTORE_PASSWORD` 时报 `Incomplete release signing configuration: provide all of ... (missing: GOPLAY_KEYSTORE, GOPLAY_KEY_ALIAS, GOPLAY_KEY_PASSWORD)`）。

本机实测（未设置任何 `GOPLAY_KEYSTORE*`）：`app-release.apk` 由 debug keystore 签名（签名者 `CN=Android Debug`），`adb install` 可用；正式上架请按上面的方式提供自己的 keystore。

生成自己的 release keystore（只做一次，务必备份；丢失后无法更新已上架应用）：

```bat
keytool -genkeypair -v -keystore C:\keys\goplay-release.jks -alias goplay ^
  -keyalg RSA -keysize 4096 -validity 10000
```

校验签名：

```bat
$env:ANDROID_HOME\build-tools\35.0.0\apksigner.bat verify --print-certs app\build\outputs\apk\release\app-release.apk
```

发布建议：

- **Google Play**：使用 Play App Signing，上传 `bundleRelease` 生成 AAB（`gradlew.bat :app:bundleRelease`，产物 `app/build/outputs/bundle/release/app-release.aab`），上传密钥自行保管。
- **第三方商店/自托管下载**：用同一 release keystore 签 APK；`versionCode`/`versionName` 在 `app/build.gradle.kts` 的 `defaultConfig` 中递增。
- 密钥只放本机环境变量、`keystore.properties` 或 CI secret，**不要**写进会提交的 `gradle.properties`。

### 测试

JVM 单元测试（无需设备）覆盖：

- `MessageCodecTest`：hello/ack/pong/human 编解码、包装/直接动作、包装层 id 继承（供 ack 关联）、缺失 action、未知字段、非法 JSON。
- `FrameCodecTest`：GPF1 往返、魔数/长度/空 JPEG/坏 header。
- `BackoffPolicyTest`：500→1000→…→30000 的无抖动序列、±20% 范围、封顶、负 attempt。
- `ActionMapperTest`：各动作映射、默认值/夹取、坐标与文本校验、不支持动作。

运行：`gradlew.bat :app:testDebugUnitTest`，报告在 `app/build/reports/tests/testDebugUnitTest/index.html`。

**已知验证结果**（2026-11，命令行实测）：`gradlew.bat :app:testDebugUnitTest` 执行成功，30 个 JVM 单元测试全部通过（0 失败/0 跳过）；同一构建链也完成了 debug/release APK 构建。尚未在真机/模拟器运行。

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
- 本机核心提供：`GET /api/local/catalog`（全部可选模型）、`POST /api/local/download {model_id}`、`DELETE /api/local/models/{id}`、`POST /api/local/select {model_id}`、`POST /api/local/start|stop|cancel`。
- 控制台「AI 大脑」会合并档位推荐与可选目录，支持下载多个模型、切换、删除；运行中删除/切换会返回 409 或先停旧服务。
- **模型权重不打包进 APK**；首次使用由手机上的 Go 核心下载到应用数据目录。
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

1. **未真机验证**：APK 已编译、JVM 测试通过，但 MediaProjection/无障碍/悬浮窗/连续语音/本机模型下载尚未在真机端到端验证；不要据此认为可直接发布。
2. **安全窗口/DRM**：银行、支付、部分游戏的安全窗口、DRM 视频、FLAG_SECURE 页面会返回黑屏或空白；这是 Android 平台保护，客户端不绕过，也不提供 root/注入方案。
3. **厂商限制**：部分 OEM 会杀后台、限制悬浮窗、限制无障碍、要求额外“后台弹出界面/自启动”权限；前台服务与通知已声明，但仍需用户手动放行。
4. **MediaProjection 授权**：每次开始截屏都要系统确认；旋转/分辨率变化会重建 VirtualDisplay；某些设备在切换应用后可能停止投影。Android 14+ 对 FGS 启动时机有严格要求。
5. **文本输入**：只对已聚焦的可编辑节点执行 `ACTION_SET_TEXT`，不会弹出/操作输入法；游戏自绘输入框或 WebView 非标准编辑器可能失败。
6. **无障碍能力**：手势可能被系统“无障碍超时”、多指操作、游戏反作弊/安全策略拒绝；`dispatchGesture` 一次只能有一个手势，客户端按顺序处理，不并行。
7. **网络与安全**：当前无 TLS、无认证、默认明文局域网；不要在公网暴露 8420。WebSocket 只支持文本控制 + 二进制帧；核心必须实现对应端点。
8. **模型下载未实测**：目录/下载/删除/切换 API 已实现并有单测，但未在手机网络上真实下载大文件；弱网/断点续传行为需真机验证。
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