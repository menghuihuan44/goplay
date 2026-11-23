# GoPlay Mobile ⇄ Go Core 协议 v1

本文件定义 Android 客户端（`com.goplay.mobile`）与 Go 核心之间的通信契约。
它是 **客户端侧 v1 提案/实现说明**：Go 核心的 WebSocket 与 REST 端点需要按此实现或适配。
Android 侧的唯一常量与编解码实现位于：

- `app/src/main/java/com/goplay/mobile/protocol/Models.kt`（常量与 DTO）
- `app/src/main/java/com/goplay/mobile/protocol/MessageCodec.kt`（文本 JSON）
- `app/src/main/java/com/goplay/mobile/protocol/FrameCodec.kt`（二进制帧）
- `app/src/main/java/com/goplay/mobile/protocol/ActionMapper.kt`（动作校验）
- `app/src/main/java/com/goplay/mobile/protocol/BackoffPolicy.kt`（重连退避）

协议版本号为 **1**。若核心不支持该版本，建议返回 `{"type":"error","error":"unsupported protocol"}` 并关闭连接；客户端会把错误显示在日志与状态栏。

---

## 1. 传输与默认地址

| 用途 | 默认值 |
|---|---|
| WebSocket | `ws://<host>:8420/ws/instance/<instanceId>` |
| REST | `http://<host>:8420` |
| 实例 ID 默认值 | `android-1` |
| 端口默认值 | `8420` |
| 客户端名 | `android` |
| 协议版本 | `1` |

`<host>` 与端口可在 App 设置页修改。当前只支持明文 `ws://`/`http://`；见“安全说明”。

WebSocket 握手请求头（客户端发送）：

| Header | 值 | 说明 |
|---|---|---|
| `X-GoPlay-Instance` | 实例 ID | 与路径参数一致 |
| `X-GoPlay-Client` | `android` | 客户端类型 |
| `X-GoPlay-Protocol` | `1` | 协议版本 |
| `X-GoPlay-Wifi-Only` | `1` / `0` | 仅 Wi-Fi 下载偏好；Android 不下载模型，核心可据此决定首次模型下载 |

服务器应把每条连接绑定到一个实例；连接关闭时不应杀掉实例，除非核心自身策略决定如此。

`X-GoPlay-Wifi-Only` 也会出现在所有 REST 请求上。Android APK 不打包模型、也不会自己下载模型，因此该值只是把设置页的策略告诉核心；真正的下载限制应由核心执行。

---

## 2. 会话时序

```text
Android                                          Go core
   |  WebSocket upgrade (+ headers)                  |
   |----------------------------------------------->|
   |  101 Switching Protocols                       |
   |<-----------------------------------------------|
   |  TEXT hello                                    |
   |----------------------------------------------->|
   |  TEXT {"type":"action", ...} / ping / stats    |
   |<-----------------------------------------------|
   |  BINARY GPF1 + JSON + JPEG                     |
   |----------------------------------------------->|
   |  TEXT ack（每个动作一个）                        |
   |----------------------------------------------->|
   |  TEXT pong（回应 ping）                          |
   |----------------------------------------------->|
   |  TEXT human（用户接管 true/false，见 3.4）        |
   |----------------------------------------------->|
```

- 客户端在收到 HTTP 101 / OkHttp `onOpen` 后立刻发送一条 `hello` 文本消息，只有 `hello` 入队成功才认为连接 ready。
- 帧与动作可以双向交错；动作按到达顺序处理，但手势本身通过 Android 异步 `dispatchGesture` 执行，因此客户端用 `id` 关联 ack。
- 客户端在 socket 关闭后按指数退避重连（见第 8 节）。
- 服务端下发的未知 `type` 会被忽略或交给 UI 日志，不会导致断开。

---

## 3. 文本消息：客户端 → 核心

### 3.1 hello（连接后第一条）

```json
{
  "type": "hello",
  "protocol": 1,
  "client": "android",
  "clientVersion": "0.1.0",
  "instanceId": "android-1",
  "device": "Android 15 (SDK 35) · Google Pixel 8 · arm64-v8a · 7912 MB RAM · 8 核",
  "capabilities": {
    "screenshot": "jpeg",
    "gestures": ["tap", "swipe", "long_press", "back", "text", "wait"],
    "maxFps": 30
  }
}
```

`device` 是便于人读的单行摘要；核心不应解析它做机器决策，机器字段可从 REST `/api/local/recommend` 的请求体获得（RAM/ABI）。

### 3.2 ack（每个动作一个）

```json
{
  "type": "ack",
  "id": "act-42",
  "ok": true,
  "actionType": "tap",
  "executedMs": 73
}
```

失败：

```json
{
  "type": "ack",
  "id": "act-43",
  "ok": false,
  "actionType": "swipe",
  "errorCode": "gesture_timeout",
  "error": "gesture callback did not complete in time",
  "executedMs": 5000
}
```

- `id` 原样回传。包装形式中若内层 `action.id` 缺失，客户端会继承外层 `id`；两层都没有时才生成 `android-<nanoTime>`（核心无法预先关联）。因此**建议每个动作都带 `id`**。
- `ok=true` 仅表示 Android 手势 API 报告完成，不保证游戏内产生了业务效果。
- `executedMs` 为从开始映射/派发到回调完成的耗时。

### 3.3 pong

收到 `{"type":"ping"}` 后回复：

```json
{"type":"pong","ts":1730000000123}
```

`ts` 是客户端当前 Unix 毫秒时间。客户端不主动发起应用层 ping；WebSocket 层由 OkHttp 每 15 秒发送 ping 帧。

### 3.4 human（用户接管信号）

客户端无障碍服务只检查 `AccessibilityEvent.eventType`，不读取事件文本或窗口内容；检测到真实用户触摸/点击/长按/滚动/文本变化时发送：

```json
{"type":"human","active":true,"reason":"accessibility"}
```

- 第一次检测到用户操作时立即发送 `active=true`；用户持续操作期间每 **1 秒**刷新一次 `true`（心跳），避免核心的安静计时器在长操作中误恢复 AI。
- 用户最后一次操作后约 **2 秒**发送 `active=false`。
- AI 通过 `dispatchGesture` 执行动作的窗口内、以及动作回调结束后 **400 ms** 的静默期内，忽略无障碍事件，避免把 AI 注入的手势误判为用户接管。
- 核心收到 `active=true` 应立即交还控制权（进入 human_control）；若 **2.5 秒**内没有新的 human 信号，核心可自动恢复 AI。客户端心跳周期 1 秒，低于该阈值。
- 服务断开/销毁时若仍处于 human 状态，会补发 `active=false`。
- 该信号只是控制权提示，不包含任何键值、坐标、文本或窗口内容。

---

## 4. 文本消息：核心 → 客户端

### 4.1 action（两种等价形式）

推荐包装形式：

```json
{
  "type": "action",
  "id": "act-42",
  "action": {
    "id": "act-42",
    "type": "tap",
    "x": 0.512,
    "y": 0.834,
    "note": "点击开始按钮"
  }
}
```

也接受“直接动作对象”（`type` 直接是动作名）：

```json
{"type":"tap","id":"act-42","x":0.512,"y":0.834}
```

别名：`click` 等同 `tap`；`longpress` 等同 `long_press`；`sleep` 等同 `wait`。
若 `type` 为 `action` 但缺少 `action` 对象，客户端记录 `action message is missing the action object` 并忽略该条。包装形式的内层动作未带 `id` 时，客户端使用外层 `id` 作为关联 id（见 3.2）。

### 4.2 动作字段与语义

坐标全部是 **归一化浮点数**，相对当前显示区域，范围 `[0.0, 1.0]`；`x=0` 为左边缘，`y=0` 为上边缘。客户端会按当前屏幕像素换算并夹在 `[1, size-2]`，避免边缘手势失效。

| `type` | 必填字段 | 可选字段 | 行为 |
|---|---|---|---|
| `tap` / `click` | `x`, `y` | `id`, `timeoutMs`, `note` | 单点按下/抬起，固定约 60 ms |
| `swipe` | `x`, `y`, `x2`, `y2` | `durationMs`（默认 300，夹在 50–10000）, `timeoutMs` | 从 (x,y) 线性滑到 (x2,y2) |
| `long_press` / `longpress` | `x`, `y` | `durationMs`（默认 700，夹在 300–5000）, `timeoutMs` | 长按 |
| `back` | — | `id`, `timeoutMs`, `note` | `GLOBAL_ACTION_BACK` |
| `text` | `text` | `id`, `timeoutMs`, `note` | 对当前焦点输入框执行 `ACTION_SET_TEXT` |
| `wait` / `sleep` | — | `durationMs`（默认 1000，夹在 0–60000） | 客户端本地等待后立即 ack，不经过手势 |

约束：

- `text` 不能为 null，且 UTF-16 长度不超过 **2000**，否则错误码 `missing_field` / `text_too_long`。
- 所有坐标必须是有限数且在 [0,1]，否则 `invalid_coords`。
- `timeoutMs` 被夹在 **1000–15000 ms**，默认 5000；超过则 ack `gesture_timeout`。
- 不支持的 `type` 返回 `unsupported_action`，不会尝试注入。
- 客户端不会读取其他应用的视图树内容；`text` 只对**已经获得输入焦点**的可编辑节点生效。如果目标输入框未聚焦，返回 `no_focus`，核心应先发 `tap`。
- `canRetrieveWindowContent` 虽在无障碍配置中开启（为了找到焦点节点），但 `onAccessibilityEvent` 不采集事件内容。

### 4.3 ping

```json
{"type":"ping"}
```

客户端回 `pong`。也允许核心携带任意附加字段；客户端忽略未知字段。

### 4.4 stats（可选）

```json
{
  "type": "stats",
  "stats": {
    "framesSent": 1200,
    "framesDropped": 40,
    "actionsExecuted": 88,
    "actionErrors": 2,
    "wsReconnects": 1,
    "captureFps": 9.8,
    "lastFrameAtMs": 1730000000123
  }
}
```

客户端目前把 `captureFps` 显示为会话 FPS，并把实例状态标记为 `running`；其余字段保留在协议中供后续使用。客户端自己的指标（截图/发送/丢弃/动作延迟/重连）独立统计，见 README。

### 4.5 chat / status / error

```json
{"type":"chat","text":"我已观察画面，准备点击开始。"}
```

`chat.text` 会写入客户端日志与消息区。`status` 可作为通用状态字符串。`error` 会记录到错误状态：

```json
{"type":"error","error":"model not loaded"}
```

### 4.6 其他

未知 `type` 的消息会被转交 UI；不会断开连接。服务器发来的二进制消息目前只记录大小并忽略。

---

## 5. 二进制帧：JPEG + header

每张截图作为 **一条 WebSocket binary message** 发送，避免 base64 膨胀，并让 header 与 JPEG 原子对应。

### 5.1 布局

```text
offset 0       4 bytes    ASCII "GPF1"（GoPlay Frame v1）
offset 4       4 bytes    uint32 big-endian N = header JSON 字节数
offset 8       N bytes    UTF-8 JSON FrameHeader
offset 8+N     M bytes    JPEG 原始字节
```

限制（编码/解码双方都应校验）：

- header JSON `N <= 65536` 字节。
- JPEG `M > 0` 且 `M <= 8 * 1024 * 1024` 字节。
- 帧总长至少 `8 + N + 1`。

### 5.2 FrameHeader

```json
{
  "capturedAtMs": 1730000000123,
  "width": 960,
  "height": 540,
  "quality": 70,
  "rotation": 0,
  "sequence": 42,
  "source": "media_projection",
  "instanceId": "android-1",
  "bytes": 48321
}
```

| 字段 | 类型 | 含义 |
|---|---|---|
| `capturedAtMs` | long | 设备 Unix 毫秒时间戳，用于端到端延迟估算 |
| `width`, `height` | int | JPEG 像素尺寸（已按最大边缩放） |
| `quality` | int | JPEG 质量 10–95 |
| `rotation` | int | 屏幕旋转角度（0/90/180/270） |
| `sequence` | long | 客户端每帧递增，从 1 开始，用于发现丢帧 |
| `source` | string | 固定 `media_projection`，便于未来扩展 |
| `instanceId` | string? | 客户端覆盖为当前实例 ID |
| `bytes` | int? | JPEG 字节数；编码时由客户端按实际值覆盖 |

### 5.3 背压与丢帧

客户端仅在以下条件同时满足时发送帧：

1. WebSocket ready（hello 已成功入队）；
2. OkHttp 发送队列为空（`queueSize() == 0`）。

否则该帧被计为 **dropped**，不会无限堆积。目标 FPS 默认为 10，可在设置页调整为 1–30；实际 FPS 受设备编码能力、分辨率、网络影响。服务端可以只处理最新帧，无需逐帧回复。

---

## 6. REST 控制面

所有请求 `Content-Type: application/json; charset=utf-8`，并带 `X-GoPlay-Wifi-Only: 1|0`（见 1 节）。客户端把非 2xx 响应的正文前 300 字符作为错误显示，不会崩溃。

### 6.1 暂停 / 继续

`POST /api/control/pause`

```json
{"instanceId":"android-1","reason":"overlay"}
```

`POST /api/control/resume`

```json
{"instanceId":"android-1"}
```

语义建议：

- pause 后核心停止产生新动作（或进入观察模式），但 WebSocket 可保持连接，客户端仍可发帧。
- resume 恢复推理/动作。
- 客户端暂停期间**仍保持连接，但不上报新帧**（截屏循环丢弃帧）；这是“停止看画面”的本地节流，不代表核心已暂停。核心应以 REST 状态为准更新实例状态。

### 6.2 用户消息

`POST /api/message`

```json
{"instanceId":"android-1","text":"先领每日奖励，再开始对战","kind":"user"}
```

响应正文可以是 `{"ok":true}` 或任意核心 JSON；客户端原样显示。

### 6.3 本地模型状态 / 推荐

`GET /api/local/status`

响应示例（具体字段由核心后续实现，客户端目前原样展示）：

```json
{"modelsDir":"C:/Users/me/.goplay/models","serverPort":8421,"activeTier":"q4_7b","downloading":false}
```

`POST /api/local/recommend`

```json
{
  "device": "Android 15 (SDK 35) · Google Pixel 8 · arm64-v8a · 7912 MB RAM · 8 核",
  "ramMb": 7912,
  "abis": ["arm64-v8a", "armeabi-v7a"]
}
```

响应示例：

```json
{"tier":"q4_3b","reason":"8 GB RAM, arm64","downloadRequired":true,"approxBytes":2200000000}
```

约定：

- Android **不把模型打进 APK**，只展示推荐档位与状态。
- 首次下载由 Go 核心执行；Android 只显示“需要在核心侧下载”，并在开启“仅 Wi-Fi 下载”时由核心/客户端共同避免蜂窝网络下载。
- 上述两个端点由核心后续实现；端点缺失时设置页会显示 HTTP 404 错误，不影响截屏/动作链路。

### 6.4 健康检查

`GET /health` → 任意 2xx 正文；客户端用于诊断。

---

## 7. 错误码（客户端 ack）

| `errorCode` | 触发条件 |
|---|---|
| `missing_field` | `text` 为 null 等 |
| `text_too_long` | 文本超过 2000 字符 |
| `invalid_coords` | 坐标缺失、非有限、超出 [0,1] |
| `unsupported_action` | 未知动作类型 |
| `accessibility_unavailable` | 无障碍服务未启用/未连接 |
| `dispatch_failed` | `dispatchGesture` 返回 false（另一手势进行中或服务被禁用） |
| `gesture_build_failed` | 构造 `GestureDescription` 异常 |
| `gesture_cancelled` | 系统取消手势 |
| `gesture_timeout` | 超时未收到手势回调 |
| `back_failed` | `GLOBAL_ACTION_BACK` 返回 false |
| `no_focus` | 没有焦点输入节点 |
| `set_text_failed` | `ACTION_SET_TEXT` 被节点拒绝 |
| `unsupported_here` | `wait` 被错误地交给无障碍服务（正常由 ActionRouter 处理，不应出现） |

---

## 8. 断线与重连

- 打开超时：**15 秒**；OkHttp 连接超时 10 秒，读超时无限（长连接），WS ping 间隔 15 秒。
- 连接成功后 `attempt` 重置为 0。
- 断线后若设置 `autoReconnect=true`：等待 `min(30_000, 500 * 2^attempt)` ms，再叠加 **±20% 抖动**，最小 1 ms；然后重连。
- 抖动使用可注入的 `Random`，JVM 单元测试覆盖了无抖动序列与范围。
- 手动停止（服务停止/用户点停止）不重连。
- 连续失败时 `attempt` 递增，理论上限为 2^32 次尝试的夹取，实际总延迟封顶 30 秒。
- 重连次数计入客户端指标 `wsReconnects`。

客户端不做应用层心跳超时判定；依赖 OkHttp 的 WebSocket ping/pong 与 read failure。

---

## 9. 安全说明

- 协议当前**没有认证/加密**，默认局域网明文。仅应在可信局域网使用，或通过 SSH 端口转发/VPN/Tailscale 等隧道访问，不要把 8420 暴露到公网。
- 客户端拒绝 `file://`、`content://` 等非 HTTP(S) 的 REST 基址（URL 由 host/port 拼接，且 host 输入做字符校验）。
- 核心应校验 `X-GoPlay-Instance` 与路径参数一致，限制 JSON 大小、JPEG 大小与动作频率；Android 侧已限制 frame header 64 KiB、JPEG 8 MiB、文本 2000 字符、坐标 [0,1]。
- 所有手势只通过 Android 无障碍 API；不注入进程、不读写游戏内存、不挂钩图形 API、不加载内核驱动。

---

## 10. 版本演进

- 新增可选 JSON 字段：保持 v1，客户端 `ignoreUnknownKeys=true`。
- 改变二进制布局或动作语义：升 `Protocol.VERSION`，并在 hello/header 中协商；当前客户端遇到版本不匹配只能显示错误，不会降级。
- 新增动作类型时必须同时更新 `ActionMapper`、`Capabilities.gestures` 与本文件；旧客户端会返回 `unsupported_action`，这是预期的安全失败。
