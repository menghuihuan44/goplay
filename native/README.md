# GoPlay Windows 原生层（native/）

本目录是 GoPlay 的 **外部、用户态** Windows 辅助层，只做三类事：

1. 截取桌面图像（GDI BitBlt，DXGI Desktop Duplication 可用时优先）。
2. 用 `SendInput` 合成键鼠/文本输入。
3. 附带一个无边框置顶状态浮窗 + 托盘菜单（`goplay-overlay.exe`）。

**安全边界（硬约束）**：不注入游戏进程、不读写游戏内存、不挂钩图形 API、
不加载未签名内核驱动、不使用 WebView2。所有实现只调用公开的 Win32/DXGI API。

---

## 1. 文件与产物

```
native/
  include/goplay_native.h     稳定的 C ABI（DLL 唯一公开头文件）
  src/capture.cpp             GDI + 可选 DXGI 截屏
  src/input.cpp               SendInput 键鼠/文本
  src/overlay.cpp             浮窗、托盘、HTTP 轮询/暂停恢复
  src/overlay.h               overlay 内部接口
  src/main.cpp                goplay-overlay.exe 入口、单实例、命令行
  tests/capture_smoke.cpp     截屏 smoke test（可手跑）
  tests/input_smoke.cpp       输入 smoke test（默认安全模式）
  CMakeLists.txt              生成 DLL + overlay + tests
  build.ps1                   一键构建/测试
  README.md                   本文件
```

构建产物（`native/build/`）：

| 产物 | 说明 |
| --- | --- |
| `goplay_native.dll` | capture + input C ABI，供 Go cgo 调用 |
| `libgoplay_native.dll.a` | MinGW 导入库，cgo `-lgoplay_native` 使用 |
| `goplay-overlay.exe` | 状态浮窗/托盘程序 |
| `capture_smoke.exe` / `input_smoke.exe` | 手动 smoke test |

Release 构建使用 `-static-libgcc -static-libstdc++ -static`，DLL/EXE **不依赖**
`libstdc++-6.dll`、`libgcc_s_seh-1.dll`、`libwinpthread-1.dll`。
只依赖 Windows 自带的 `d3d11.dll`、`GDI32`、`USER32`、`KERNEL32`、UCRT
api-set，以及 overlay 的 `WINHTTP`、`SHELL32`。

注意：`goplay_native.dll` 的导入表已干净（无任何进程/线程/内存 API）。
`goplay-overlay.exe` 因使用 `std::wstring`/std::string，静态 libstdc++ 的异常
分配器会通过 libwinpthread 带进三个**从未被 GoPlay 代码调用**的 KERNEL32 导入项，
详见 §6 的二进制审计与 §7 第 13 条。

---

## 2. 依赖与本机构建

要求：

- Windows 10/11 x64（UCRT；不支持 Windows 7）。
- MSYS2 UCRT64 工具链：`g++`（本机实测 16.2.0）、`ninja`。
- CMake >= 3.20（本机实测 4.1.0）。
- PowerShell 5.1+（仅 `build.ps1` 需要）。

让 `C:\msys64\ucrt64\bin`、CMake 的 `bin` 和 Ninja 在 `PATH` 中，然后在仓库根目录：

```powershell
.\native\build.ps1 -Test
```

如果 PowerShell 默认禁止运行脚本（报错 `running scripts is disabled on this system`），
用进程级绕过，不需要改系统策略：

```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .\native\build.ps1 -Test
```

等价的显式命令：

```powershell
cmake -S native -B native/build -G Ninja `
  -DCMAKE_BUILD_TYPE=Release `
  -DCMAKE_CXX_COMPILER=g++ `
  -DGOPLAY_BUILD_TESTS=ON
cmake --build native/build --parallel
ctest --test-dir native/build --output-on-failure
```

MSYS2 UCRT64 shell 中：

```bash
cd /c/Codes/goplay
cmake -S native -B native/build -G Ninja -DCMAKE_BUILD_TYPE=Release \
      -DCMAKE_CXX_COMPILER=g++ -DGOPLAY_BUILD_TESTS=ON
cmake --build native/build --parallel
```

只构建 DLL/overlay、不构建测试：加 `-DGOPLAY_BUILD_TESTS=OFF`。
`build.ps1` 参数：`-BuildDir build`、`-Configuration Release`、`-Clean`、`-Test`。

### 手动 smoke test

```powershell
# 截屏：抓 3 帧，检查 BGRA 尺寸；可选写 PPM 便于肉眼确认
.\native\build\capture_smoke.exe --ppm .\capture.ppm

# 输入：默认只做无副作用调用（移到当前位置、0 滚轮、空文本、参数校验）
.\native\build\input_smoke.exe

# 会真的点击一次并发送 F24（可见副作用），仅在需要时用
.\native\build\input_smoke.exe --unsafe

# overlay：启动约 0.9 秒后自动退出，不阻塞
.\native\build\goplay-overlay.exe --self-test --self-test-ms 900
```

`ctest` 只跑两个非交互 smoke test，不会实际点击。

---

## 3. Overlay 使用

```powershell
.\native\build\goplay-overlay.exe --instance <instance-id>
```

| 参数 / 环境变量 | 说明 |
| --- | --- |
| `--instance <id>` / `--instance-id <id>` | 指定要暂停/恢复的实例；省略时从 `GET /api/instances` 取第一个 `id` |
| `--base-url <url>` / `--api-url` / `--url` | API 根地址，默认 `http://127.0.0.1:8420` |
| `GOPLAY_ADDR` | 仅当未显式传 `--base-url` 时，作为 `http://${GOPLAY_ADDR}` 使用 |
| `--self-test` | 启动后自动退出（构建验证用） |
| `--self-test-ms <N>` | 自动退出延迟，夹在 100..30000 ms |
| `--help` / `--version` | 弹窗说明 / 版本 |

行为：

- 无边框、`WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_LAYERED | WS_EX_NOACTIVATE`
  的置顶浮窗，启动时位于主屏工作区右上角；拖动标题区域可移动。
- 按钮：**Open Web UI**（`ShellExecuteW` 打开 base URL）、
  **Pause/Resume**（POST `/api/instances/<id>/pause|resume`）、**Hide**。
- 点击不会抢游戏/浏览器焦点（`MA_NOACTIVATE`）；Hide 后可从托盘恢复。
- 托盘：双击显示/隐藏；右键菜单“Hide/Show overlay、Open Web UI、Exit”。
- 单实例：命名互斥体 `Local\GoPlayOverlay.Singleton`；第二次启动会把已有
  窗口广播唤出后立即退出（退出码 0）。
- 不做 WebView2；状态文字是原生 GDI 绘制。

### 对 Go API 的假设

overlay 只作为 HTTP 客户端（WinHTTP，**显式绕过系统代理**），每 2 秒轮询：

```
GET  /api/instances
       -> JSON 对象或数组，包含 "id" 字段（取第一个出现值）
GET  /api/instances/<urlencoded-id>
       -> 可选 "paused": true|false、"status": "running" 等
POST /api/instances/<urlencoded-id>/pause
POST /api/instances/<urlencoded-id>/resume
       -> 2xx 视为成功；body 为 {}
```

如果 Go 服务端尚未实现这些路由，浮窗会显示 offline / HTTP 错误，但程序本身不崩溃。
overlay 不会自己启动 Go 服务，也不监听任何端口。

---

## 4. C ABI 参考

头文件：`native/include/goplay_native.h`。x86-64 Windows、`__cdecl`、C linkage。
所有函数都是线程安全的（capture 内部有全局互斥；input 无共享状态）。

### 4.1 错误码

| 宏 | 值 | 含义 |
| --- | ---: | --- |
| `GP_OK` | 0 | 成功 |
| `GP_ERR_INVALID_ARG` | -1 | 参数非法/显示器索引越界 |
| `GP_ERR_NOT_INITIALIZED` | -2 | 未调用 `gp_capture_init` |
| `GP_ERR_WIN32` | -3 | Win32/DXGI 调用失败 |
| `GP_ERR_UNSUPPORTED` | -4 | 当前系统/桌面不支持该路径 |
| `GP_ERR_NO_MEMORY` | -5 | 分配失败 |
| `GP_ERR_TIMEOUT` | -6 | DXGI 在超时前没有新帧（且无缓存帧） |
| `GP_ERR_UTF8` | -7 | 非法 UTF-8 |
| `GP_ERR_TOO_LARGE` | -8 | 文本超过 1,048,576 个 UTF-16 单元 |

`gp_capture_last_error()` / `gp_input_last_error()` 返回**本线程**最近一次失败的
Win32 `GetLastError()` 或 HRESULT 数值（失败于 GP_ERR_WIN32 时有意义）。

### 4.2 Capture

```c
int  gp_capture_init(int monitor);              /* 0=主屏；1..N 按 EnumDisplayMonitors 顺序 */
int  gp_capture_frame(unsigned char** data, int* w, int* h, int* stride);
void gp_capture_free(unsigned char* data);
void gp_capture_close(void);
int  gp_capture_backend(void);                  /* 0=none 1=GDI 2=DXGI */
int  gp_capture_monitor_count(void);
const char* gp_capture_backend_name(void);      /* "none" / "gdi" / "dxgi" */
int  gp_capture_last_error(void);
```

- 每次成功返回的 `*data` 是 `malloc()` 的独立缓冲区，调用方必须
  `gp_capture_free()`，不能 free 后复用。
- 布局固定：**top-down BGRA，32bpp，stride == width * 4**。
- 失败时 `*data == NULL`，`w/h/stride` 都置 0。
- 初始化优先 DXGI；DXGI 初始化失败、格式不是 BGRA、或运行中
  `ACCESS_LOST`/其他错误时自动回退 GDI。运行期回退后本次 init 生命周期内
  保持 GDI；重新 `gp_capture_init` 可再试 DXGI。
- DXGI 50ms 内无新帧且已有缓存帧时返回缓存；无缓存帧时该帧改走 GDI。
- 设置环境变量 `GP_NATIVE_DISABLE_DXGI=1` 可强制 GDI（测试/排障）。
- 截屏不包含鼠标指针（GDI BitBlt 和 Desktop Duplication 纹理都不含指针）。
- 需要已登录、未锁屏的交互式桌面；锁屏/UAC 安全桌面/受保护内容不可截取。

### 4.3 Input

```c
int gp_input_init(void);
int gp_input_move(int x, int y);
int gp_input_move_rel(int dx, int dy);
int gp_input_click(int x, int y, int button, int count);
int gp_input_down(int button);
int gp_input_up(int button);
int gp_input_key_down(int vk);
int gp_input_key_up(int vk);
int gp_input_key_press(int vk, int hold_ms);
int gp_input_key_scan(int scan, int extended, int down);
int gp_input_text(const wchar_t* text);
int gp_input_text_utf8(const char* text);
int gp_input_wheel(int delta);
int gp_input_last_error(void);
```

- 鼠标键：`GP_MOUSE_LEFT=0`、`RIGHT=1`、`MIDDLE=2`、`X1=3`、`X2=4`。
- `gp_input_move` 使用虚拟桌面绝对坐标；超出桌面会被 SendInput 归一化截断。
- `gp_input_click`：`x=y=-1` 表示不移动；`count` 夹在 1..20；按住约 20ms，
  连击间隔约 40ms。坐标只给一个 -1 会返回 `GP_ERR_INVALID_ARG`。
- 键盘 `vk` 范围 1..254；`hold_ms` 夹在 0..10000。
- `gp_input_key_scan` 发送 set-1 scan code（0..255），适合 DirectInput 游戏。
- 文本按 UTF-16 单元用 `KEYEVENTF_UNICODE` 发送；`\n` 转成 `VK_RETURN`，
  `\r` 忽略。单次最多 1,048,576 个 UTF-16 单元；UTF-8 版本先严格校验
  （拒绝 overlong、代理区、>U+10FFFF）。
- 滚轮 delta 夹在 ±12000，正数向上。
- 所有事件只走 `SendInput`；受 Windows UIPI 限制，非提权进程不能向提权窗口
  发送输入，本库不提供任何绕过。

---

## 5. Go cgo 用法

假设 Go 包在 `internal/native/`，则 `native/` 在仓库根目录。按实际包深度调整
`${SRCDIR}` 相对路径：

```go
package native

/*
#cgo CFLAGS: -I${SRCDIR}/../../native/include
#cgo LDFLAGS: -L${SRCDIR}/../../native/build -lgoplay_native
#include <stdlib.h>
#include "goplay_native.h"
*/
import "C"

import (
    "fmt"
    "unsafe"
)

func Capture(monitor int) (pix []byte, w, h int, err error) {
    if rc := C.gp_capture_init(C.int(monitor)); rc != C.GP_OK {
        return nil, 0, 0, fmt.Errorf("gp_capture_init: %d (win32=%d)",
            int(rc), int(C.gp_capture_last_error()))
    }
    defer C.gp_capture_close()

    var data *C.uchar
    var cw, ch, stride C.int
    if rc := C.gp_capture_frame(&data, &cw, &ch, &stride); rc != C.GP_OK {
        return nil, 0, 0, fmt.Errorf("gp_capture_frame: %d (win32=%d)",
            int(rc), int(C.gp_capture_last_error()))
    }
    defer C.gp_capture_free(data)

    n := int(stride) * int(ch)
    return C.GoBytes(unsafe.Pointer(data), C.int(n)), int(cw), int(ch), nil
}

func Click(x, y int) error {
    if rc := C.gp_input_click(C.int(x), C.int(y), C.GP_MOUSE_LEFT, 1); rc != C.GP_OK {
        return fmt.Errorf("gp_input_click: %d", int(rc))
    }
    return nil
}
```

构建/运行：

```powershell
$env:CGO_ENABLED = "1"
$env:CC = "C:\msys64\ucrt64\bin\gcc.exe"
go build -o goplay.exe .
Copy-Item native/build/goplay_native.dll .   # 或把 native/build 加入 PATH
.\goplay.exe
```

Windows 加载器只会在 exe 目录、系统目录和 `PATH` 中找 `goplay_native.dll`；
**发布时把 DLL 放在 Go 可执行文件旁边**。DLL 已静态链接 MinGW 运行库，
不需要额外分发 MSYS2 的 `.dll`。

注意：Go 进程如果不是 DPI-aware，显示器坐标可能被 Windows 虚拟化。生产入口
建议在 `main` 最开始调用：

```go
// x/sys/windows 或 syscall 调用 SetProcessDpiAwarenessContext
// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = -4
```

overlay 自己在 `WinMain` 里调用了 `SetProcessDPIAware()`。

---

## 6. 安全审计点

可用下面的命令复核（在仓库根目录）：

```powershell
Select-String -Path native/src/*.cpp,native/src/*.h,native/include/*.h `
  -Pattern 'OpenProcess|ReadProcessMemory|WriteProcessMemory|CreateRemoteThread|SetWindowsHookEx|NtReadVirtualMemory|NtWriteVirtualMemory|VirtualAllocEx|SetThreadContext|GetThreadContext|MinHook|Detours'
```

预期：只命中注释里的“禁止 API 名单”，没有实际调用。本机实测命中
`native/src/capture.cpp:54`、`native/src/input.cpp:5-6`、`native/src/input.cpp:23`，
全部是注释/说明文字。

### 二进制导入审计

用 `objdump -p native/build/goplay_native.dll | Select-String "DLL Name|OpenProcess|ReadProcessMemory|..."`
复核。本机结果：

- `goplay_native.dll`：只导入 `d3d11.dll`、`GDI32.dll`、`KERNEL32.dll`、
  `USER32.dll`、`api-ms-win-crt-*`；**没有** `OpenProcess`、
  `GetThreadContext`、`SetThreadContext`、`ReadProcessMemory`、
  `WriteProcessMemory`、`CreateRemoteThread`、`SetWindowsHookEx`。
  为达到这一点，capture/input 不用 `std::vector`/`std::wstring`/`std::thread`，
  改用固定数组、`malloc/realloc`、SRWLOCK 和 SendInput。
- `goplay-overlay.exe`：导入表仍含 KERNEL32 的 `OpenProcess`、
  `GetThreadContext`、`SetThreadContext`。来源是 MinGW 静态 libstdc++：
  `wstring-inst.o`/eh 抛出路径 -> `eh_alloc.o` -> `libwinpthread`
  （`pthread_mutex_init`）。overlay 只用 `std::wstring`/`std::string` 做 URL 和
  JSON 字符串，没有 `std::thread`/`std::mutex`；源码审计与链接 map 都表明这三个
  导入不会被调用，属于静态运行库的“死亡导入”。
  已尝试 `-fno-exceptions`、`-Wl,--gc-sections`、把 `std::mutex/thread` 换成
  Win32 原语、以及自定义 `__throw_*`/operator new，均未能消除；彻底消除需要
  重写 overlay 全部字符串或改为动态分发 libstdc++/libwinpthread（多 3 个 DLL）。
  当前选择静态分发 + 如实披露。若安全审计要求 **导入表零命中** 且不区分是否调用，
  应改用 MSVC 构建 overlay，或接受动态运行库方案。

实际使用的敏感/Win32 API：

- `capture.cpp`：`EnumDisplayMonitors`、`GetMonitorInfoW`、`GetDC`/`BitBlt`、
  `D3D11CreateDevice`、`IDXGIOutput1::DuplicateOutput`、`AcquireNextFrame`、
  `ID3D11DeviceContext::Map`。
- `input.cpp`：`SendInput`、`MapVirtualKeyW`、`GetSystemMetrics`。
- `overlay.cpp`：`ShellExecuteW`（打开默认浏览器）、WinHTTP（只连用户给的
  base URL）、`Shell_NotifyIconW`、GDI 绘制、`CreateMutexW`（单实例）。
- 没有 `LoadLibrary` 注入、没有驱动、没有图形 API hook、没有 WebView2、
  没有监听端口。
- 产物没有签名；SmartScreen/杀软可能对“模拟输入”程序提示，这是预期行为。
- overlay 的 `--base-url` 会被 `ShellExecuteW` 打开，也会被 WinHTTP 访问；
  只接受 http/https。启动参数应视为可信输入，不要从不受信任的远程内容拼接。

---

## 7. 已知限制与风险

1. **本机 DXGI 实际回退 GDI**：开发机桌面是 HDR
   （Desktop Duplication 输出 `DXGI_FORMAT_R16G16B16A16_FLOAT`），当前实现只接受
   `B8G8R8A8_UNORM/_SRGB`，因此自动回退 GDI。GDI 路径已实测 2880x1800 BGRA 正常；
   DXGI 的编译/链接/回退逻辑已验证，但 HDR 下的 DXGI 取帧未在本机跑到。
2. DXGI 运行期 `ACCESS_LOST` 后本次 init 内永久回退 GDI；需要重新
   `gp_capture_init` 才能再尝试 DXGI。
3. 截屏不含鼠标指针；需要指针时由上层另行绘制/获取。
4. `BitBlt` 依赖交互式桌面；锁屏、UAC 安全桌面、受 DRM 保护内容抓不到。
5. DPI：非 per-monitor DPI-aware 的宿主可能拿到虚拟化坐标/尺寸；Go 入口应
   自行设置 DPI awareness。overlay 只做了 system-DPI aware，跨不同缩放显示器
   时不会自动重排。
6. `SendInput` 受 UIPI 限制，不能向提权窗口注入；没有管理员/驱动绕过。
7. `gp_input_click` 会同步 Sleep（约 20ms/次，连击再加 40ms），大批量点击时
   调用线程会被阻塞；上层应放到自己的工作线程。
8. `gp_input_text` 用 UTF-16 单元发送，非 BMP 字符按代理对发送，极少数只接受
   IME 组合输入的程序可能表现不同；不提供剪贴板/IME 模拟。
9. overlay 关闭时会 join HTTP 工作线程；如果 base URL 指向一个“黑洞”地址，
   最坏要等当前 WinHTTP 请求超时（单请求 800/800/1500/1500ms 量级），
   退出可能延迟数秒。本机 8420 端口无服务、`--self-test-ms 900` 时，多次实测
   总耗时约 2.1–6.3 秒（含启动、一次失败轮询和线程退出），但都会自动退出，
   不阻塞构建。
10. overlay 的 API 路由/JSON 字段是约定；Go 服务端若改了路径或返回结构，
    需要同步修改 `overlay.cpp` 或让服务端兼容。
11. 没有自定义图标/本地化文案（使用 `IDI_APPLICATION` 和英文按钮）。
12. 未做代码签名；发布安装包时需自行签名。
13. `goplay-overlay.exe` 的导入表含三个来自 MinGW 静态 libstdc++ 的死亡导入
    （`OpenProcess`、`GetThreadContext`、`SetThreadContext`），源码没有任何
    调用；`goplay_native.dll` 不受影响。详见 §6。

---

## 8. 构建验证记录（本机）

- 工具链：MSYS2 UCRT64 `g++ 16.2.0`、CMake `4.1.0`、Ninja `1.13.2`。
- `cmake -S native -B native/build -G Ninja -DCMAKE_BUILD_TYPE=Release -DCMAKE_CXX_COMPILER=g++ -DGOPLAY_BUILD_TESTS=ON`：成功。
- `cmake --build native/build --parallel`：成功，无警告。
- `ctest --test-dir native/build --output-on-failure`：2/2 通过
  （capture_smoke 0.90s、input_smoke 0.13s）。
- `capture_smoke`：1 个显示器，GDI 后端，连续 3 帧 2880x1800、stride 11520、
  BGRA 字节数 20,736,000，PASS。
- `input_smoke`（safe）：全部返回预期值（含非法 UTF-8 = -7、非法参数 = -1），PASS。
- `goplay-overlay.exe --self-test --self-test-ms 900`：定时器到点后销毁窗口，
  `Start-Process -Wait` 实测约 2.1 秒退出（本机 8420 无服务，含一次失败轮询），
  退出码 0。多次运行在 2.1–6.3 秒之间。
- 单实例：先启动 6 秒 self-test 的实例，再启动第二个；第二个立即退出（0），
  第一个正常退出（0）。
- DLL 导出 22 个 `gp_capture_*`/`gp_input_*` C 符号；依赖仅系统 DLL（静态
  MinGW 运行库后不再依赖 `libstdc++-6.dll` 等）。DLL 导入表已审计，无禁用 API；
  overlay EXE 的死亡导入见 §6/§7.13。