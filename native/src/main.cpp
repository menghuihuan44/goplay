/*
 * main.cpp - goplay-overlay.exe entry point / demo launcher.
 *
 * The actual window, tray icon and HTTP worker live in overlay.cpp.  This file
 * owns command-line parsing, DPI setup and the single-instance mutex.
 */
#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>
#include <shellapi.h>

#include "overlay.h"

#include <string>

namespace {

constexpr wchar_t kMutexName[] = L"Local\\GoPlayOverlay.Singleton";
constexpr wchar_t kShowExistingMessage[] = L"GoPlayOverlay.ShowExisting";

void ShowHelp() {
    MessageBoxW(
        nullptr,
        L"GoPlay Overlay\n\n"
        L"Usage: goplay-overlay.exe [options]\n\n"
        L"  --instance <id>     instance id used for pause/resume\n"
        L"  --base-url <url>    Web API base (default http://127.0.0.1:8420)\n"
        L"  --self-test         start, then exit automatically after ~1.2 s\n"
        L"  --self-test-ms <N>  auto-exit delay in milliseconds\n"
        L"  --help              this message\n"
        L"  --version           print version\n\n"
        L"Environment: GOPLAY_ADDR overrides the default API address.",
        L"GoPlay Overlay", MB_OK | MB_ICONINFORMATION);
}

bool ParseCommandLine(OverlayOptions& options) {
    int argc = 0;
    LPWSTR* argv = CommandLineToArgvW(GetCommandLineW(), &argc);
    if (!argv) return true;

    bool ok = true;
    for (int i = 1; i < argc; ++i) {
        const std::wstring arg = argv[i];
        auto next = [&](std::wstring& target) {
            if (i + 1 < argc) {
                target = argv[++i];
                return true;
            }
            ok = false;
            return false;
        };
        if (arg == L"--instance" || arg == L"--instance-id") {
            if (!next(options.instance_id)) break;
        } else if (arg == L"--base-url" || arg == L"--api-url" ||
                   arg == L"--url") {
            if (!next(options.base_url)) break;
        } else if (arg == L"--self-test") {
            options.self_test = true;
        } else if (arg == L"--self-test-ms") {
            std::wstring value;
            if (!next(value)) break;
            options.self_test_ms = _wtoi(value.c_str());
            if (options.self_test_ms < 100) options.self_test_ms = 100;
            if (options.self_test_ms > 30000) options.self_test_ms = 30000;
        } else if (arg == L"--help" || arg == L"-h" || arg == L"/?") {
            ShowHelp();
            ok = false;
            break;
        } else if (arg == L"--version") {
            MessageBoxW(nullptr, L"GoPlay Overlay 0.1.0", L"GoPlay Overlay",
                        MB_OK | MB_ICONINFORMATION);
            ok = false;
            break;
        } else {
            // Ignore unknown switches rather than failing launcher scripts.
        }
    }
    LocalFree(argv);

    while (!options.base_url.empty() && options.base_url.back() == L'/') {
        options.base_url.pop_back();
    }
    if (options.base_url.empty()) options.base_url = L"http://127.0.0.1:8420";

    const bool http = options.base_url.rfind(L"http://", 0) == 0;
    const bool https = options.base_url.rfind(L"https://", 0) == 0;
    if (!http && !https) ok = false;
    return ok;
}

void ApplyEnvironment(OverlayOptions& options) {
    if (options.base_url != L"http://127.0.0.1:8420") return;
    wchar_t addr[256]{};
    const DWORD length = GetEnvironmentVariableW(
        L"GOPLAY_ADDR", addr,
        static_cast<DWORD>(sizeof(addr) / sizeof(addr[0])));
    if (length > 0 && length < sizeof(addr) / sizeof(addr[0])) {
        options.base_url = std::wstring(L"http://") + addr;
    }
}

}  // namespace

int WINAPI WinMain(HINSTANCE instance, HINSTANCE, LPSTR, int) {
    SetProcessDPIAware();

    OverlayOptions options;
    ApplyEnvironment(options);
    if (!ParseCommandLine(options)) {
        return 2;
    }

    HANDLE mutex = CreateMutexW(nullptr, TRUE, kMutexName);
    if (!mutex) return 5;
    const DWORD create_error = GetLastError();
    if (create_error == ERROR_ALREADY_EXISTS) {
        const UINT message = RegisterWindowMessageW(kShowExistingMessage);
        if (message != 0) {
            PostMessageW(HWND_BROADCAST, message, 0, 0);
        }
        ReleaseMutex(mutex);
        CloseHandle(mutex);
        return 0;
    }

    const int result = gp_overlay_run(instance, options);

    ReleaseMutex(mutex);
    CloseHandle(mutex);
    return result;
}
