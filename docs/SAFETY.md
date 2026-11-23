# 安全边界

## 承诺

GoPlay 的感知与操作只使用操作系统公开接口：

- Windows：GDI/DXGI 桌面复制、@SendInput@、@GetLastInputInfo@、@GetSystemMetrics@；
- Android：MediaProjection 录屏、AccessibilityService 手势；
- 网络：本地 HTTP/WebSocket，或用户显式配置的 AI API。

**不做**：注入目标进程、@OpenProcess/ReadProcessMemory/WriteProcessMemory@、@CreateRemoteThread@、APC/线程上下文注入、图形 API 钩子（D3D/OpenGL/Vulkan）、内核驱动、反作弊规避、修改游戏文件。

## 威胁模型

- 默认只监听 @127.0.0.1:8420@，面板与 API 没有认证。任何能访问该端口的本机进程都可以控制实例。
- 若把 @addr@ 改为 @0.0.0.0@ 或局域网地址，必须自行加认证、TLS、防火墙或反向代理；本项目不承诺公网安全。
- Android 客户端通过 @ws://@ 连接，局域网内同样应使用可信网络；生产部署应加 TLS 与配对码。
- 本地模型只监听 loopback；llama-server 不接受外部连接。
- API Key 存在配置文件或环境变量中；@/api/config@ 返回 redacted 配置，但本机文件权限仍需自行保护。

## 人类接管

人工输入优先。Windows 使用 @GetLastInputInfo@ 检测最近输入（默认 1200ms 窗口），Android 使用无障碍触摸事件。检测到人工操作时：

1. 引擎进入 @human_control@，不再执行动作；
2. 已发出的单个动作可能已在途，无法撤回；下一个 tick 起冻结；
3. 人类空闲达到 @human_resume_ms@（默认 2500ms）后自动恢复；
4. 事件与指标记录接管次数和延迟。

这不是“暂停游戏”，而是把操作权交还用户。用户也可以显式 @pause@。

## 静态审计

```powershell
go run ./cmd/goplay-audit -json -out reports/audit-report.json
```

审计器执行两件事：

1. **源码扫描**：遍历 Go/C/C++/Kotlin/Lua，去掉注释但保留字符串字面量，匹配危险符号：@OpenProcess@、@ReadProcessMemory@、@WriteProcessMemory@、@CreateRemoteThread@、@VirtualAllocEx@、@SetThreadContext@、@GetThreadContext@、@NtReadVirtualMemory@、@ptrace@、@/proc/<pid>/mem@ 等。字符串字面量保留是为了发现 @GetProcAddress("OpenProcess")@ 这类动态解析。
2. **PE 导入表解析**：只解析文件，不加载/执行；检查 DLL/EXE 的导入符号，critical 级失败，review 级警告。

当前结果：129 个源码文件 0 findings；10 个 PE 0 critical；16 条 review 来自 Go runtime / MinGW C++ runtime 的 @OpenProcess@、@GetThreadContext@、@SetThreadContext@ 导入。项目源码没有调用这些 API，审计把 review 与 critical 分开，避免把运行库误报当成注入证据。报告扫描的是当前工作区，包含 @bin/@ 与 @native/build/@ 的构建产物。

允许列表：@internal/audit/**@、@reports/**@、@**/testdata/**@。审计器自身包含这些敏感字符串，是扫描器而非调用者。

## 动作安全

- 所有动作先过 @policy.Validator@：类型白名单、风险上限、归一化坐标范围；
- 全局速率限制默认 8 动作/秒；
- 未知动作不会被“发明”给执行器；
- 每个平台执行器只实现自己支持的输入类型，不支持的类型返回错误；
- Lua profile 只能生成 @domain.Action@，不能直接调用系统 API。加载时移除 @os/io/debug/package/channel@ 与 @dofile/loadfile/require@，只保留 base/table/string/math（见 @luabridge.sandbox@）。内置 profile 仍是可信代码，不把任意第三方 Lua 当安全沙箱执行。

## 隐私

截屏可能包含敏感信息。默认只在本机处理；若启用视觉 LLM，帧会发送到你配置的 API。面板/API 不把图像写入磁盘；Android 帧只在内存中保留最新一帧，超过 1.5s 会被引擎重新等待新帧。本地模型模式下图像与文本都不离开本机（除非你配置了远程 API）。

## 负责任使用

只在你拥有或获授权使用的游戏/设备上运行。遵守游戏服务条款与当地法律。不要用本项目规避反作弊、刷取真实资产或干扰其他玩家。外部截屏+输入在多数单机/模拟器场景可用，但网络游戏可能禁止自动化，是否允许由游戏规则决定。
