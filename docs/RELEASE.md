# GoPlay 一键打包与发布

`cmd/goplay-release` 是唯一的发布入口，CI 与本地使用同一条命令。它负责：跑测试（可选）、构建 Go 二进制、构建 Windows 原生组件、调用 Gradle 构建 Android APK、生成压缩包、写 `SHA256SUMS.txt` 与 `release.json`。

## 1. 快速命令

```powershell
# 当前平台 GUI + Android + 交叉编译 CLI
go run ./cmd/goplay-release -target all -out dist

# 只构建 Windows 便携包（含 GUI、DLL、CLI）
go run ./cmd/goplay-release -target windows -out dist

# 只构建 Android（SDK 可显式指定）
go run ./cmd/goplay-release -target android -android-sdk C:\Android\sdk -out dist

# 只交叉编译 Linux/macOS CLI
go run ./cmd/goplay-release -target cli -out dist

# 完整测试 + 清理旧产物 + 全部目标
go run ./cmd/goplay-release -target all -clean -out dist
```

`-target` 支持逗号分隔：`windows,android`、`cli`、`all`。`all` 的含义是“当前宿主 GUI + Android + CLI”，不会在 Windows 上伪造 Linux/macOS GUI。

## 2. 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `-target` | `all` | all/windows/android/linux/darwin/cli，可逗号分隔 |
| `-version` | VERSION 文件 | 版本号，写入文件名与 `-ldflags` |
| `-out` | `dist` | 输出目录 |
| `-root` | `.` | 仓库根目录 |
| `-skip-tests` | false | 跳过 `go test ./...`（CI 已单独跑测试时用） |
| `-skip-native` | false | 跳过 Windows C++ 构建 |
| `-skip-android` | false | 跳过 Android |
| `-android-sdk` | 自动探测 | SDK 路径 |
| `-bundle` | false | 额外构建 Android AAB |
| `-clean` | false | 先删除输出目录 |
| `-version-info` | — | 打印工具版本并退出 |

版本注入使用 `-ldflags "-X goplay/internal/version.Version=..."`；所有二进制共享同一版本。

## 3. 产物

### Windows

`GoPlay-<version>-windows-amd64.zip`：

- `GoPlay.exe`：Windows GUI 子系统桌面壳（cgo + WebView2）；
- `goplayd.exe`：无界面 daemon；
- `goplay-cli.exe`：控制 CLI（**特意改名**，避免与大小写不敏感的 `GoPlay.exe` 冲突）；
- `goplay-model.exe`、`goplay-eval.exe`、`goplay-audit.exe`；
- `goplay_native.dll`、`goplay-overlay.exe`；
- README/LICENSE/config.example.json/docs。

### Android

`GoPlay-<version>-android.apk`（release）与 `GoPlay-<version>-android-debug.apk`。若未配置签名，release APK 回退 debug 签名：可直接安装内测，**不能上架**。

### CLI

`GoPlay-<version>-linux-amd64-cli.tar.gz`、`-linux-arm64-cli`、`-darwin-amd64-cli`、`-darwin-arm64-cli`。这些是纯 CLI，不包含 GUI。

### 元数据

- `SHA256SUMS.txt`：每个产物的 SHA256；
- `release.json`：版本、commit、构建时间、产物大小与哈希。

## 4. Android 签名

按以下顺序读取四项：`GOPLAY_KEYSTORE`、`GOPLAY_KEYSTORE_PASSWORD`、`GOPLAY_KEY_ALIAS`、`GOPLAY_KEY_PASSWORD`。

- 四项齐全：使用 release keystore；
- 四项全空：回退 debug 签名（会打印警告）；
- 只提供一部分：构建失败并列出缺失项。

keystore 不得提交。CI 中放入 Secrets，或在本机用环境变量注入。

## 5. GitHub Actions

`.github/workflows/release.yml` 在推送 `v*` 标签或手动触发时运行四个 job：

| Job | Runner | 产物 |
|---|---|---|
| windows | windows-2022 + MSYS2 UCRT64 | Windows zip |
| linux | ubuntu-22.04 + WebKitGTK | Linux tar.gz（GUI） |
| macos | macos-14 | macOS tar.gz（GUI） |
| android | ubuntu-22.04 + JDK17 + SDK35 | APK/AAB |

`publish` job 汇总全部 artifact，生成总 `SHA256SUMS.txt`；tag 构建时创建 GitHub Release。模型权重与 llama.cpp 运行时不进入任何产物。

本地开发用 `-skip-tests` 可快速验证打包；CI 在打包前单独跑 `go test ./...`，避免重复。

## 6. 发布检查清单

1. 更新 `VERSION` 与 `android/app/build.gradle.kts` 的 `versionCode/versionName`。
2. `go test ./... -count=1` 与 `go test -race ./internal/engine ./internal/api ./internal/memory ./internal/scheduler` 通过。
3. `go run ./cmd/goplay-eval -quick -json -report-dir reports` 与 `go run ./cmd/goplay-audit -json -out reports/audit-report.json` 通过。
4. `go run ./cmd/goplay-release -target all -out dist` 成功；解压 Windows 包，双击 `GoPlay.exe`，确认窗口和 `/api/health` 正常。
5. 用 `apksigner verify --print-certs` 检查 APK；在真机安装并授予录屏/无障碍/悬浮窗权限。
6. 提交源码、文档、CI、VERSION；不要提交 `dist/`、`bin/`、`android/.gradle`、`android/app/build`、keystore、模型权重、运行时临时目录。
7. 打 tag `v<version>` 并推送，等待 Actions 创建 Release。

## 7. 已知限制

- Linux/macOS 的桌面 GUI 必须在对应系统构建；跨平台只保证 CLI。
- Windows GUI 依赖 WebView2 Runtime；无运行时可退回 `goplayd` + 浏览器。
- Android release 签名需要自备 keystore；无 SDK 的机器只能跳过 Android。
- MinGW 构建的浮窗 exe 会因运行库导入 `OpenProcess` 等符号，但项目源码不调用；安全审计将其列为 review。
