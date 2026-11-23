/*
 * overlay.cpp - borderless always-on-top status overlay for GoPlay.
 *
 * UI: a small per-pixel-alpha-free layered window (constant alpha + rounded
 * window region) with three buttons:
 *   - Open Web UI : ShellExecuteW(http://127.0.0.1:8420)
 *   - Pause/Resume: HTTP POST /api/instances/<id>/pause|resume
 *   - Hide        : hide the window (tray icon stays)
 *
 * Tray icon: show/hide, open Web UI, exit.  Single instance via a named mutex.
 * No WebView2, no injection, no graphics hooks.
 */
#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>
#include <windowsx.h>
#include <shellapi.h>
#include <winhttp.h>

#include "goplay_native.h"  // not strictly needed; keeps the DLL/header in sync
#include "overlay.h"

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <deque>
#include <memory>
#include <string>
#include <utility>
#include <vector>

namespace {

constexpr wchar_t kWindowClass[] = L"GoPlayNativeOverlayWindow";
constexpr wchar_t kShowExistingMessage[] = L"GoPlayOverlay.ShowExisting";
constexpr wchar_t kUserAgent[] = L"GoPlayOverlay/0.1";
constexpr UINT kTrayMessage = WM_APP + 2;
constexpr UINT kStatusMessage = WM_APP + 3;
constexpr UINT_PTR kSelfTestTimer = 1;
constexpr size_t kMaxHttpBody = 1024 * 1024;

const COLORREF kBackground = RGB(22, 24, 30);
const COLORREF kBorder = RGB(58, 63, 76);
const COLORREF kText = RGB(232, 235, 242);
const COLORREF kMuted = RGB(148, 154, 168);
const COLORREF kButton = RGB(40, 44, 55);
const COLORREF kButtonHot = RGB(56, 62, 78);
const COLORREF kButtonPressed = RGB(68, 76, 96);
const COLORREF kButtonDisabled = RGB(31, 33, 41);
const COLORREF kAccent = RGB(86, 156, 255);
const COLORREF kGreen = RGB(76, 214, 130);
const COLORREF kRed = RGB(255, 98, 98);
const COLORREF kAmber = RGB(245, 184, 76);

OverlayOptions g_options;

struct StatusInfo {
    bool online = false;
    bool paused = false;
    bool have_paused = false;
    std::wstring status_text;
    std::wstring detail;
    std::wstring instance_id;
};

StatusInfo g_status;

HINSTANCE g_instance = nullptr;
HWND g_hwnd = nullptr;
HICON g_tray_icon = nullptr;
bool g_tray_added = false;
UINT g_taskbar_created_message = 0;
UINT g_show_existing_message = 0;

HFONT g_font_title = nullptr;
HFONT g_font_body = nullptr;
HFONT g_font_small = nullptr;
HFONT g_font_button = nullptr;

int g_dpi = 96;
int g_window_w = 340;
int g_window_h = 150;

RECT g_btn_open{};
RECT g_btn_pause{};
RECT g_btn_hide{};

int g_hot_button = 0;
int g_pressed_button = 0;

enum ButtonId {
    kButtonNone = 0,
    kButtonOpen = 1,
    kButtonPause = 2,
    kButtonHide = 3,
};

enum TrayCommand {
    kTrayToggle = 100,
    kTrayOpen = 101,
    kTrayExit = 102,
};

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

int S(int value) {
    return MulDiv(value, g_dpi, 96);
}

std::wstring WideFromUtf8(const std::string& text) {
    if (text.empty()) return std::wstring();
    const int needed = MultiByteToWideChar(CP_UTF8, 0, text.data(),
                                           static_cast<int>(text.size()), nullptr, 0);
    if (needed <= 0) return std::wstring();
    std::wstring out(static_cast<size_t>(needed), L'\0');
    MultiByteToWideChar(CP_UTF8, 0, text.data(), static_cast<int>(text.size()),
                        out.data(), needed);
    return out;
}

std::string Utf8FromWide(const std::wstring& text) {
    if (text.empty()) return std::string();
    const int needed = WideCharToMultiByte(CP_UTF8, 0, text.data(),
                                           static_cast<int>(text.size()), nullptr,
                                           0, nullptr, nullptr);
    if (needed <= 0) return std::string();
    std::string out(static_cast<size_t>(needed), '\0');
    WideCharToMultiByte(CP_UTF8, 0, text.data(), static_cast<int>(text.size()),
                        out.data(), needed, nullptr, nullptr);
    return out;
}

std::wstring EscapePathSegment(const std::wstring& segment) {
    static const char* hex = "0123456789ABCDEF";
    const std::string utf8 = Utf8FromWide(segment);
    std::string out;
    out.reserve(utf8.size() + 8);
    for (unsigned char c : utf8) {
        const bool unreserved = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
                                (c >= '0' && c <= '9') || c == '-' || c == '.' ||
                                c == '_' || c == '~';
        if (unreserved) {
            out.push_back(static_cast<char>(c));
        } else {
            out.push_back('%');
            out.push_back(hex[(c >> 4) & 0x0F]);
            out.push_back(hex[c & 0x0F]);
        }
    }
    return WideFromUtf8(out);
}

std::wstring JoinUrl(const std::wstring& base, const std::wstring& path) {
    std::wstring result = base;
    while (!result.empty() && result.back() == L'/') result.pop_back();
    if (path.empty()) return result;
    if (path.front() == L'/') return result + path;
    return result + L"/" + path;
}

int DpiOfWindow(HWND hwnd) {
    HDC dc = GetDC(hwnd);
    if (!dc) return 96;
    const int dpi = GetDeviceCaps(dc, LOGPIXELSY);
    ReleaseDC(hwnd, dc);
    return dpi > 0 ? dpi : 96;
}

// ---------------------------------------------------------------------------
// Minimal JSON field extraction (enough for the local API's flat objects)
// ---------------------------------------------------------------------------

void SkipWs(const std::string& s, size_t& p) {
    while (p < s.size() && (s[p] == ' ' || s[p] == '\t' || s[p] == '\r' ||
                            s[p] == '\n')) {
        ++p;
    }
}

bool FindKey(const std::string& s, const std::string& key, size_t& value_pos) {
    const std::string quoted = "\"" + key + "\"";
    size_t pos = 0;
    while ((pos = s.find(quoted, pos)) != std::string::npos) {
        size_t p = pos + quoted.size();
        SkipWs(s, p);
        if (p < s.size() && s[p] == ':') {
            ++p;
            SkipWs(s, p);
            value_pos = p;
            return true;
        }
        pos = p;
    }
    return false;
}

bool ExtractJsonBool(const std::string& s, const std::string& key, bool& out) {
    size_t p = 0;
    if (!FindKey(s, key, p)) return false;
    if (s.compare(p, 4, "true") == 0) {
        out = true;
        return true;
    }
    if (s.compare(p, 5, "false") == 0) {
        out = false;
        return true;
    }
    return false;
}

void AppendUtf8(std::string& out, unsigned int cp) {
    if (cp <= 0x7F) {
        out.push_back(static_cast<char>(cp));
    } else if (cp <= 0x7FF) {
        out.push_back(static_cast<char>(0xC0 | (cp >> 6)));
        out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
    } else if (cp <= 0xFFFF) {
        out.push_back(static_cast<char>(0xE0 | (cp >> 12)));
        out.push_back(static_cast<char>(0x80 | ((cp >> 6) & 0x3F)));
        out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
    } else {
        out.push_back(static_cast<char>(0xF0 | (cp >> 18)));
        out.push_back(static_cast<char>(0x80 | ((cp >> 12) & 0x3F)));
        out.push_back(static_cast<char>(0x80 | ((cp >> 6) & 0x3F)));
        out.push_back(static_cast<char>(0x80 | (cp & 0x3F)));
    }
}

int HexDigit(char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

bool ExtractJsonString(const std::string& s, const std::string& key,
                       std::string& out) {
    size_t p = 0;
    if (!FindKey(s, key, p)) return false;
    if (p >= s.size() || s[p] != '"') return false;
    ++p;
    out.clear();
    while (p < s.size()) {
        const char c = s[p++];
        if (c == '"') return true;
        if (c != '\\') {
            out.push_back(c);
            continue;
        }
        if (p >= s.size()) return false;
        const char esc = s[p++];
        switch (esc) {
            case '"': out.push_back('"'); break;
            case '\\': out.push_back('\\'); break;
            case '/': out.push_back('/'); break;
            case 'b': out.push_back('\b'); break;
            case 'f': out.push_back('\f'); break;
            case 'n': out.push_back('\n'); break;
            case 'r': out.push_back('\r'); break;
            case 't': out.push_back('\t'); break;
            case 'u': {
                if (p + 4 > s.size()) return false;
                unsigned int cp = 0;
                for (int i = 0; i < 4; ++i) {
                    const int d = HexDigit(s[p + static_cast<size_t>(i)]);
                    if (d < 0) return false;
                    cp = (cp << 4) | static_cast<unsigned int>(d);
                }
                p += 4;
                if (cp >= 0xD800 && cp <= 0xDBFF && p + 6 <= s.size() &&
                    s[p] == '\\' && s[p + 1] == 'u') {
                    unsigned int low = 0;
                    bool ok = true;
                    for (int i = 0; i < 4; ++i) {
                        const int d = HexDigit(s[p + 2 + static_cast<size_t>(i)]);
                        if (d < 0) {
                            ok = false;
                            break;
                        }
                        low = (low << 4) | static_cast<unsigned int>(d);
                    }
                    if (ok && low >= 0xDC00 && low <= 0xDFFF) {
                        cp = 0x10000 + ((cp - 0xD800) << 10) + (low - 0xDC00);
                        p += 6;
                    }
                }
                AppendUtf8(out, cp);
                break;
            }
            default:
                out.push_back(esc);
                break;
        }
    }
    return false;
}

// ---------------------------------------------------------------------------
// WinHTTP client (synchronous, short timeouts; runs on the worker thread)
// ---------------------------------------------------------------------------

bool HttpRequest(const std::wstring& url, const wchar_t* method,
                 const std::string& request_body, int& http_status,
                 std::string& response, std::wstring& error) {
    http_status = 0;
    response.clear();
    error.clear();

    URL_COMPONENTS components{};
    components.dwStructSize = sizeof(components);
    wchar_t host[256]{};
    wchar_t path[2048]{};
    wchar_t extra[1024]{};
    components.lpszHostName = host;
    components.dwHostNameLength = static_cast<DWORD>(sizeof(host) / sizeof(host[0]));
    components.lpszUrlPath = path;
    components.dwUrlPathLength = static_cast<DWORD>(sizeof(path) / sizeof(path[0]));
    components.lpszExtraInfo = extra;
    components.dwExtraInfoLength =
        static_cast<DWORD>(sizeof(extra) / sizeof(extra[0]));

    if (!WinHttpCrackUrl(url.c_str(), static_cast<DWORD>(url.size()), 0,
                         &components)) {
        error = L"invalid URL";
        return false;
    }

    std::wstring target = path;
    if (components.dwExtraInfoLength > 0) target += extra;
    if (target.empty()) target = L"/";

    const bool secure = components.nScheme == INTERNET_SCHEME_HTTPS;
    INTERNET_PORT port = components.nPort;
    if (port == 0) port = secure ? INTERNET_DEFAULT_HTTPS_PORT
                                 : INTERNET_DEFAULT_HTTP_PORT;

    // The overlay talks to the local GoPlay API; never route that through a
    // system proxy (a corporate proxy would otherwise break 127.0.0.1 calls).
    HINTERNET session = WinHttpOpen(
        kUserAgent, WINHTTP_ACCESS_TYPE_NO_PROXY, WINHTTP_NO_PROXY_NAME,
        WINHTTP_NO_PROXY_BYPASS, 0);
    if (!session) {
        error = L"WinHttpOpen failed";
        return false;
    }
    WinHttpSetTimeouts(session, 800, 800, 1500, 1500);

    HINTERNET connect =
        WinHttpConnect(session, host, port, 0);
    if (!connect) {
        error = L"WinHttpConnect failed";
        WinHttpCloseHandle(session);
        return false;
    }

    HINTERNET request = WinHttpOpenRequest(
        connect, method, target.c_str(), nullptr, WINHTTP_NO_REFERER,
        WINHTTP_DEFAULT_ACCEPT_TYPES, secure ? WINHTTP_FLAG_SECURE : 0);
    if (!request) {
        error = L"WinHttpOpenRequest failed";
        WinHttpCloseHandle(connect);
        WinHttpCloseHandle(session);
        return false;
    }

    const bool is_post = _wcsicmp(method, L"POST") == 0;
    const wchar_t* headers =
        is_post ? L"Content-Type: application/json\r\n" : WINHTTP_NO_ADDITIONAL_HEADERS;
    const DWORD headers_len = is_post ? static_cast<DWORD>(-1L) : 0;
    LPVOID body_ptr = is_post
                          ? (request_body.empty()
                                 ? WINHTTP_NO_REQUEST_DATA
                                 : const_cast<char*>(request_body.data()))
                          : WINHTTP_NO_REQUEST_DATA;
    const DWORD body_len =
        is_post ? static_cast<DWORD>(request_body.size()) : 0;

    BOOL ok = WinHttpSendRequest(request, headers, headers_len, body_ptr,
                                 body_len, body_len, 0);
    if (ok) ok = WinHttpReceiveResponse(request, nullptr);

    if (ok) {
        DWORD status = 0;
        DWORD status_size = sizeof(status);
        if (WinHttpQueryHeaders(request,
                                WINHTTP_QUERY_STATUS_CODE |
                                    WINHTTP_QUERY_FLAG_NUMBER,
                                WINHTTP_HEADER_NAME_BY_INDEX, &status,
                                &status_size, WINHTTP_NO_HEADER_INDEX)) {
            http_status = static_cast<int>(status);
        }
        for (;;) {
            DWORD available = 0;
            if (!WinHttpQueryDataAvailable(request, &available)) break;
            if (available == 0) break;
            const DWORD chunk_size =
                std::min<DWORD>(available, 64u * 1024u);
            std::string chunk(chunk_size, '\0');
            DWORD read = 0;
            if (!WinHttpReadData(request, chunk.data(), chunk_size, &read)) break;
            if (read == 0) break;
            response.append(chunk.data(), read);
            if (response.size() >= kMaxHttpBody) break;
        }
    } else {
        error = L"network error";
    }

    WinHttpCloseHandle(request);
    WinHttpCloseHandle(connect);
    WinHttpCloseHandle(session);
    return ok != FALSE;
}

bool HttpGet(const std::wstring& url, int& status, std::string& body,
             std::wstring& error) {
    return HttpRequest(url, L"GET", std::string(), status, body, error);
}

// ---------------------------------------------------------------------------
// Background worker: polls status, performs pause/resume POSTs
// ---------------------------------------------------------------------------

class OverlayWorker {
  public:
    OverlayWorker(HWND hwnd, std::wstring base_url, std::wstring instance_id)
        : hwnd_(hwnd), base_url_(std::move(base_url)),
          instance_id_(std::move(instance_id)) {
        InitializeCriticalSection(&mutex_);
        stop_event_ = CreateEventW(nullptr, TRUE, FALSE, nullptr);
        wake_event_ = CreateEventW(nullptr, FALSE, FALSE, nullptr);
    }

    ~OverlayWorker() {
        Stop();
        if (stop_event_) CloseHandle(stop_event_);
        if (wake_event_) CloseHandle(wake_event_);
        DeleteCriticalSection(&mutex_);
    }

    bool Start() {
        if (!stop_event_ || !wake_event_) return false;
        thread_ = CreateThread(nullptr, 0, &OverlayWorker::ThreadEntry, this, 0,
                               &thread_id_);
        return thread_ != nullptr;
    }

    void Stop() {
        if (!thread_) return;
        SetEvent(stop_event_);
        WaitForSingleObject(thread_, INFINITE);
        CloseHandle(thread_);
        thread_ = nullptr;
    }

    void Request(int command) {
        EnterCriticalSection(&mutex_);
        queue_.push_back(command);
        LeaveCriticalSection(&mutex_);
        if (wake_event_) SetEvent(wake_event_);
    }

  private:
    void Publish(StatusInfo info) {
        auto* heap = new StatusInfo(std::move(info));
        if (!PostMessageW(hwnd_, kStatusMessage, 0,
                          reinterpret_cast<LPARAM>(heap))) {
            delete heap;
        }
    }

    bool ResolveInstanceId() {
        if (!instance_id_.empty()) return true;
        int status = 0;
        std::string body;
        std::wstring error;
        if (!HttpGet(JoinUrl(base_url_, L"/api/instances"), status, body, error))
            return false;
        std::string id;
        if (ExtractJsonString(body, "id", id)) {
            instance_id_ = WideFromUtf8(id);
        }
        return !instance_id_.empty();
    }

    void Poll(const std::wstring& note) {
        StatusInfo info;
        info.instance_id = instance_id_;

        int status = 0;
        std::string body;
        std::wstring error;
        if (!HttpGet(JoinUrl(base_url_, L"/api/instances"), status, body, error)) {
            info.online = false;
            info.detail = note.empty() ? L"backend unreachable; retrying" : note;
            Publish(std::move(info));
            return;
        }

        info.online = true;
        if (instance_id_.empty()) {
            std::string id;
            if (ExtractJsonString(body, "id", id)) {
                instance_id_ = WideFromUtf8(id);
                info.instance_id = instance_id_;
            }
        }

        if (!instance_id_.empty()) {
            int detail_status = 0;
            std::string detail_body;
            std::wstring detail_error;
            const std::wstring detail_url = JoinUrl(
                base_url_, L"/api/instances/" + EscapePathSegment(instance_id_));
            if (HttpGet(detail_url, detail_status, detail_body, detail_error)) {
                bool paused = false;
                if (ExtractJsonBool(detail_body, "paused", paused)) {
                    info.paused = paused;
                    info.have_paused = true;
                } else if (ExtractJsonBool(body, "paused", paused)) {
                    info.paused = paused;
                    info.have_paused = true;
                }
                std::string st;
                if (ExtractJsonString(detail_body, "status", st)) {
                    info.status_text = WideFromUtf8(st);
                }
                if (detail_status >= 400 && info.status_text.empty()) {
                    info.detail = L"instance HTTP " +
                                  std::to_wstring(detail_status);
                }
            } else if (detail_error == L"invalid URL") {
                info.detail = L"invalid instance id";
            } else {
                info.detail = L"instance status unavailable";
            }
        } else {
            info.detail = L"instance id not found";
        }

        if (!note.empty()) {
            info.detail = note;
        } else if (info.detail.empty() && status >= 400) {
            info.detail = L"API HTTP " + std::to_wstring(status);
        }
        Publish(std::move(info));
    }

    void DoCommand(int command) {
        if (!ResolveInstanceId()) {
            Poll(L"instance id not found");
            return;
        }
        const bool pause = command == 1;
        const std::wstring path =
            L"/api/instances/" + EscapePathSegment(instance_id_) +
            (pause ? L"/pause" : L"/resume");
        int status = 0;
        std::string body;
        std::wstring error;
        if (!HttpRequest(JoinUrl(base_url_, path), L"POST", "{}", status, body,
                         error)) {
            Poll(pause ? L"pause failed: no connection"
                       : L"resume failed: no connection");
            return;
        }
        if (status >= 200 && status < 300) {
            Poll(pause ? L"pause sent" : L"resume sent");
        } else {
            Poll(std::wstring(pause ? L"pause" : L"resume") + L" failed: HTTP " +
                 std::to_wstring(status));
        }
    }

    static DWORD WINAPI ThreadEntry(LPVOID user) {
        static_cast<OverlayWorker*>(user)->Run();
        return 0;
    }

    void Run() {
        using clock = std::chrono::steady_clock;
        auto next_poll = clock::now();
        for (;;) {
            const auto now = clock::now();
            DWORD timeout_ms = 0;
            if (next_poll > now) {
                const auto remain =
                    std::chrono::duration_cast<std::chrono::milliseconds>(
                        next_poll - now)
                        .count();
                if (remain >= 2000) {
                    timeout_ms = 2000;
                } else if (remain > 0) {
                    timeout_ms = static_cast<DWORD>(remain);
                }
            }

            HANDLE handles[2] = {stop_event_, wake_event_};
            const DWORD wait =
                WaitForMultipleObjects(2, handles, FALSE, timeout_ms);
            if (wait == WAIT_OBJECT_0) break;

            int command = 0;
            bool have_command = false;
            if (wait == WAIT_OBJECT_0 + 1) {
                EnterCriticalSection(&mutex_);
                if (!queue_.empty()) {
                    command = queue_.front();
                    queue_.pop_front();
                    have_command = true;
                }
                LeaveCriticalSection(&mutex_);
            }

            if (have_command) {
                DoCommand(command);
                next_poll = clock::now() + std::chrono::seconds(2);
            } else if (wait == WAIT_TIMEOUT) {
                Poll(L"");
                next_poll = clock::now() + std::chrono::seconds(2);
            } else if (wait == WAIT_FAILED) {
                Sleep(100);
                next_poll = clock::now() + std::chrono::seconds(1);
            }
        }
    }

    HWND hwnd_ = nullptr;
    std::wstring base_url_;
    std::wstring instance_id_;
    HANDLE thread_ = nullptr;
    DWORD thread_id_ = 0;
    CRITICAL_SECTION mutex_{};
    HANDLE stop_event_ = nullptr;
    HANDLE wake_event_ = nullptr;
    std::deque<int> queue_;
};

std::unique_ptr<OverlayWorker> g_worker;

// ---------------------------------------------------------------------------
// Tray icon
// ---------------------------------------------------------------------------

void UpdateTrayTip() {
    if (!g_tray_added) return;
    NOTIFYICONDATAW nid{};
    nid.cbSize = sizeof(nid);
    nid.hWnd = g_hwnd;
    nid.uID = 1;
    nid.uFlags = NIF_TIP;
    std::wstring tip = L"GoPlay Overlay - ";
    if (!g_status.online) {
        tip += L"backend offline";
    } else if (g_status.have_paused) {
        tip += g_status.paused ? L"paused" : L"running";
    } else {
        tip += L"connected";
    }
    wcsncpy(nid.szTip, tip.c_str(),
            sizeof(nid.szTip) / sizeof(nid.szTip[0]) - 1);
    Shell_NotifyIconW(NIM_MODIFY, &nid);
}

void AddTrayIcon() {
    if (g_tray_added) return;
    if (!g_tray_icon) g_tray_icon = LoadIconW(nullptr, IDI_APPLICATION);
    NOTIFYICONDATAW nid{};
    nid.cbSize = sizeof(nid);
    nid.hWnd = g_hwnd;
    nid.uID = 1;
    nid.uFlags = NIF_MESSAGE | NIF_ICON | NIF_TIP;
    nid.uCallbackMessage = kTrayMessage;
    nid.hIcon = g_tray_icon;
    wcsncpy(nid.szTip, L"GoPlay Overlay", 63);
    if (Shell_NotifyIconW(NIM_ADD, &nid)) {
        g_tray_added = true;
        UpdateTrayTip();
    }
}

void RemoveTrayIcon() {
    if (!g_tray_added) return;
    NOTIFYICONDATAW nid{};
    nid.cbSize = sizeof(nid);
    nid.hWnd = g_hwnd;
    nid.uID = 1;
    Shell_NotifyIconW(NIM_DELETE, &nid);
    g_tray_added = false;
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

void OpenWebUi() {
    const HINSTANCE result = ShellExecuteW(nullptr, L"open",
                                           g_options.base_url.c_str(), nullptr,
                                           nullptr, SW_SHOWNORMAL);
    if (reinterpret_cast<INT_PTR>(result) <= 32) {
        g_status.detail = L"could not open browser";
        InvalidateRect(g_hwnd, nullptr, TRUE);
    }
}

void ToggleVisibility() {
    if (IsWindowVisible(g_hwnd)) {
        ShowWindow(g_hwnd, SW_HIDE);
    } else {
        ShowWindow(g_hwnd, SW_SHOWNOACTIVATE);
        SetWindowPos(g_hwnd, HWND_TOPMOST, 0, 0, 0, 0,
                     SWP_NOMOVE | SWP_NOSIZE | SWP_NOACTIVATE);
        InvalidateRect(g_hwnd, nullptr, TRUE);
    }
}

void ShowTrayMenu() {
    const bool visible = IsWindowVisible(g_hwnd) != FALSE;
    HMENU menu = CreatePopupMenu();
    if (!menu) return;
    AppendMenuW(menu, MF_STRING, kTrayToggle,
                visible ? L"Hide overlay" : L"Show overlay");
    AppendMenuW(menu, MF_STRING, kTrayOpen, L"Open Web UI");
    AppendMenuW(menu, MF_SEPARATOR, 0, nullptr);
    AppendMenuW(menu, MF_STRING, kTrayExit, L"Exit");

    POINT pt{};
    GetCursorPos(&pt);

    // WS_EX_NOACTIVATE prevents focus stealing, but TrackPopupMenu wants a
    // foreground owner so the menu dismisses correctly. Temporarily allow it.
    const LONG_PTR old_style = GetWindowLongPtrW(g_hwnd, GWL_EXSTYLE);
    SetWindowLongPtrW(g_hwnd, GWL_EXSTYLE, old_style & ~WS_EX_NOACTIVATE);
    SetForegroundWindow(g_hwnd);

    const UINT command = TrackPopupMenuEx(
        menu, TPM_RIGHTBUTTON | TPM_RETURNCMD | TPM_NONOTIFY, pt.x, pt.y,
        g_hwnd, nullptr);
    PostMessageW(g_hwnd, WM_NULL, 0, 0);
    SetWindowLongPtrW(g_hwnd, GWL_EXSTYLE, old_style);
    DestroyMenu(menu);

    switch (command) {
        case kTrayToggle:
            ToggleVisibility();
            break;
        case kTrayOpen:
            OpenWebUi();
            break;
        case kTrayExit:
            DestroyWindow(g_hwnd);
            break;
        default:
            break;
    }
}

std::wstring EffectiveInstanceId() {
    if (!g_status.instance_id.empty()) return g_status.instance_id;
    return g_options.instance_id;
}

bool PauseButtonEnabled() {
    return g_status.online && !EffectiveInstanceId().empty();
}

void TogglePause() {
    if (!PauseButtonEnabled() || !g_worker) return;
    const bool pause = !(g_status.have_paused && g_status.paused);
    g_worker->Request(pause ? 1 : 2);
    g_status.detail = pause ? L"sending pause..." : L"sending resume...";
    InvalidateRect(g_hwnd, nullptr, TRUE);
}

// ---------------------------------------------------------------------------
// Painting
// ---------------------------------------------------------------------------

void FillSolid(HDC dc, const RECT& rc, COLORREF color) {
    HBRUSH brush = CreateSolidBrush(color);
    FillRect(dc, &rc, brush);
    DeleteObject(brush);
}

void RoundFill(HDC dc, const RECT& rc, int radius, COLORREF fill,
               COLORREF border) {
    HBRUSH brush = CreateSolidBrush(fill);
    HPEN pen = CreatePen(PS_SOLID, 1, border);
    HGDIOBJ old_brush = SelectObject(dc, brush);
    HGDIOBJ old_pen = SelectObject(dc, pen);
    RoundRect(dc, rc.left, rc.top, rc.right, rc.bottom, radius, radius);
    SelectObject(dc, old_brush);
    SelectObject(dc, old_pen);
    DeleteObject(brush);
    DeleteObject(pen);
}

void DrawTextLine(HDC dc, const std::wstring& text, const RECT& rc, HFONT font,
                  COLORREF color, UINT flags) {
    HGDIOBJ old = SelectObject(dc, font);
    SetTextColor(dc, color);
    SetBkMode(dc, TRANSPARENT);
    RECT copy = rc;
    DrawTextW(dc, text.c_str(), static_cast<int>(text.size()), &copy, flags);
    SelectObject(dc, old);
}

void DrawDot(HDC dc, int x, int y, int diameter, COLORREF color) {
    HBRUSH brush = CreateSolidBrush(color);
    HPEN pen = CreatePen(PS_SOLID, 1, color);
    HGDIOBJ old_brush = SelectObject(dc, brush);
    HGDIOBJ old_pen = SelectObject(dc, pen);
    Ellipse(dc, x, y, x + diameter, y + diameter);
    SelectObject(dc, old_brush);
    SelectObject(dc, old_pen);
    DeleteObject(brush);
    DeleteObject(pen);
}

void PaintWindow(HWND hwnd) {
    PAINTSTRUCT ps{};
    HDC dc = BeginPaint(hwnd, &ps);
    if (!dc) return;

    RECT client{};
    GetClientRect(hwnd, &client);
    const int width = client.right;
    const int height = client.bottom;

    HDC mem = CreateCompatibleDC(dc);
    HBITMAP bitmap = CreateCompatibleBitmap(dc, width, height);
    HGDIOBJ old_bitmap = SelectObject(mem, bitmap);

    FillSolid(mem, client, kBackground);

    // Window border (the rounded region clips the corners).
    {
        HPEN pen = CreatePen(PS_SOLID, 1, kBorder);
        HGDIOBJ old_pen = SelectObject(mem, pen);
        HGDIOBJ old_brush = SelectObject(mem, GetStockObject(NULL_BRUSH));
        RoundRect(mem, 0, 0, width, height, S(16), S(16));
        SelectObject(mem, old_brush);
        SelectObject(mem, old_pen);
        DeleteObject(pen);
    }

    DrawTextLine(mem, L"GoPlay", RECT{S(14), S(8), S(240), S(34)},
                 g_font_title, kText, DT_SINGLELINE | DT_VCENTER | DT_LEFT);
    DrawTextLine(mem, L"native overlay", RECT{S(206), S(10), S(326), S(32)},
                 g_font_small, kMuted,
                 DT_SINGLELINE | DT_VCENTER | DT_RIGHT);

    const COLORREF backend_dot = g_status.online ? kGreen : kRed;
    DrawDot(mem, S(14), S(43), S(8), backend_dot);
    const std::wstring backend_line =
        g_status.online ? L"Backend: online" : L"Backend: offline";
    DrawTextLine(mem, backend_line, RECT{S(28), S(36), S(326), S(54)},
                 g_font_body, g_status.online ? kText : kMuted,
                 DT_SINGLELINE | DT_VCENTER | DT_LEFT | DT_END_ELLIPSIS);

    const std::wstring instance_id = EffectiveInstanceId();
    const COLORREF instance_dot = instance_id.empty()
                                      ? kMuted
                                      : (g_status.have_paused && g_status.paused
                                             ? kAmber
                                             : kGreen);
    DrawDot(mem, S(14), S(61), S(8), instance_dot);
    std::wstring instance_line;
    if (instance_id.empty()) {
        instance_line = L"Instance: not selected";
    } else {
        instance_line = L"Instance: " + instance_id;
        if (g_status.have_paused) {
            instance_line += g_status.paused ? L"  (paused)" : L"  (running)";
        } else if (!g_status.status_text.empty()) {
            instance_line += L"  (" + g_status.status_text + L")";
        }
    }
    DrawTextLine(mem, instance_line, RECT{S(28), S(54), S(326), S(72)},
                 g_font_body, kText,
                 DT_SINGLELINE | DT_VCENTER | DT_LEFT | DT_END_ELLIPSIS);

    if (!g_status.detail.empty()) {
        DrawTextLine(mem, g_status.detail, RECT{S(14), S(76), S(326), S(92)},
                     g_font_small, kMuted,
                     DT_SINGLELINE | DT_VCENTER | DT_LEFT | DT_END_ELLIPSIS);
    }

    struct ButtonPaint {
        const RECT* rect;
        int id;
        std::wstring label;
        bool enabled;
        COLORREF accent;
    };
    const bool pause_enabled = PauseButtonEnabled();
    ButtonPaint buttons[] = {
        {&g_btn_open, kButtonOpen, L"Open Web UI", true, kAccent},
        {&g_btn_pause, kButtonPause,
         (g_status.have_paused && g_status.paused) ? L"Resume" : L"Pause",
         pause_enabled,
         (g_status.have_paused && g_status.paused) ? kAmber : kGreen},
        {&g_btn_hide, kButtonHide, L"Hide", true, kMuted},
    };

    for (const ButtonPaint& button : buttons) {
        COLORREF fill = button.enabled ? kButton : kButtonDisabled;
        COLORREF border = button.enabled ? kBorder : kButtonDisabled;
        if (button.enabled && g_hot_button == button.id) {
            fill = g_pressed_button == button.id ? kButtonPressed : kButtonHot;
            border = button.accent;
        }
        RoundFill(mem, *button.rect, S(8), fill, border);
        DrawTextLine(mem, button.label, *button.rect, g_font_button,
                     button.enabled ? kText : kMuted,
                     DT_SINGLELINE | DT_VCENTER | DT_CENTER);
    }

    BitBlt(dc, 0, 0, width, height, mem, 0, 0, SRCCOPY);
    SelectObject(mem, old_bitmap);
    DeleteObject(bitmap);
    DeleteDC(mem);
    EndPaint(hwnd, &ps);
}

// ---------------------------------------------------------------------------
// Window plumbing
// ---------------------------------------------------------------------------

void CreateFonts() {
    auto make = [](int points, int weight) {
        return CreateFontW(-MulDiv(points, g_dpi, 72), 0, 0, 0, weight, FALSE,
                           FALSE, FALSE, DEFAULT_CHARSET, OUT_DEFAULT_PRECIS,
                           CLIP_DEFAULT_PRECIS, CLEARTYPE_QUALITY,
                           DEFAULT_PITCH | FF_DONTCARE, L"Segoe UI");
    };
    if (g_font_title) DeleteObject(g_font_title);
    if (g_font_body) DeleteObject(g_font_body);
    if (g_font_small) DeleteObject(g_font_small);
    if (g_font_button) DeleteObject(g_font_button);
    g_font_title = make(12, FW_SEMIBOLD);
    g_font_body = make(9, FW_NORMAL);
    g_font_small = make(8, FW_NORMAL);
    g_font_button = make(9, FW_SEMIBOLD);
}

void LayoutButtons() {
    g_btn_open = RECT{S(12), S(104), S(156), S(132)};
    g_btn_pause = RECT{S(164), S(104), S(268), S(132)};
    g_btn_hide = RECT{S(276), S(104), S(328), S(132)};
}

int HitTestButton(int x, int y) {
    POINT pt{x, y};
    if (PtInRect(&g_btn_open, pt)) return kButtonOpen;
    if (PtInRect(&g_btn_pause, pt) && PauseButtonEnabled()) return kButtonPause;
    if (PtInRect(&g_btn_hide, pt)) return kButtonHide;
    return kButtonNone;
}

void InvokeButton(int id) {
    switch (id) {
        case kButtonOpen:
            OpenWebUi();
            break;
        case kButtonPause:
            TogglePause();
            break;
        case kButtonHide:
            ShowWindow(g_hwnd, SW_HIDE);
            break;
        default:
            break;
    }
}

void UpdateWindowTitle() {
    std::wstring title = L"GoPlay Overlay";
    if (g_status.online) {
        if (g_status.have_paused) {
            title += g_status.paused ? L" - paused" : L" - running";
        } else {
            title += L" - online";
        }
    } else {
        title += L" - offline";
    }
    SetWindowTextW(g_hwnd, title.c_str());
}

void PositionWindow() {
    MONITORINFO mi{};
    mi.cbSize = sizeof(mi);
    const HMONITOR monitor =
        MonitorFromWindow(g_hwnd, MONITOR_DEFAULTTOPRIMARY);
    if (!GetMonitorInfoW(monitor, &mi)) return;
    const int x = mi.rcWork.right - g_window_w - S(24);
    const int y = mi.rcWork.top + S(24);
    SetWindowPos(g_hwnd, HWND_TOPMOST, x, y, g_window_w, g_window_h,
                 SWP_NOACTIVATE | SWP_SHOWWINDOW);
}

LRESULT CALLBACK WndProc(HWND hwnd, UINT message, WPARAM wparam,
                         LPARAM lparam) {
    if (g_show_existing_message != 0 && message == g_show_existing_message) {
        ShowWindow(hwnd, SW_SHOWNOACTIVATE);
        SetWindowPos(hwnd, HWND_TOPMOST, 0, 0, 0, 0,
                     SWP_NOMOVE | SWP_NOSIZE | SWP_NOACTIVATE);
        InvalidateRect(hwnd, nullptr, TRUE);
        return 0;
    }
    if (g_taskbar_created_message != 0 &&
        message == g_taskbar_created_message) {
        g_tray_added = false;
        AddTrayIcon();
        return 0;
    }

    switch (message) {
        case WM_CREATE: {
            g_hwnd = hwnd;
            g_dpi = DpiOfWindow(hwnd);
            g_window_w = S(340);
            g_window_h = S(150);
            CreateFonts();
            LayoutButtons();

            HRGN region = CreateRoundRectRgn(0, 0, g_window_w + 1,
                                             g_window_h + 1, S(16), S(16));
            if (region) SetWindowRgn(hwnd, region, TRUE);

            SetLayeredWindowAttributes(hwnd, 0, 242, LWA_ALPHA);
            PositionWindow();

            if (!g_tray_icon) g_tray_icon = LoadIconW(nullptr, IDI_APPLICATION);
            AddTrayIcon();

            g_worker = std::make_unique<OverlayWorker>(
                hwnd, g_options.base_url, g_options.instance_id);
            g_worker->Start();

            if (g_options.self_test) {
                SetTimer(hwnd, kSelfTestTimer,
                         static_cast<UINT>(g_options.self_test_ms), nullptr);
            }
            InvalidateRect(hwnd, nullptr, TRUE);
            return 0;
        }

        case WM_PAINT:
            PaintWindow(hwnd);
            return 0;

        case WM_ERASEBKGND:
            return 1;

        case WM_MOUSEACTIVATE:
            // Never steal focus from the game / browser.
            return MA_NOACTIVATE;

        case WM_SETCURSOR: {
            if (LOWORD(lparam) == HTCLIENT) {
                POINT pt{};
                GetCursorPos(&pt);
                ScreenToClient(hwnd, &pt);
                if (HitTestButton(pt.x, pt.y) != kButtonNone) {
                    SetCursor(LoadCursorW(nullptr, IDC_HAND));
                    return TRUE;
                }
            }
            break;
        }

        case WM_NCHITTEST: {
            LRESULT hit = DefWindowProcW(hwnd, message, wparam, lparam);
            if (hit == HTCLIENT) {
                POINT pt{GET_X_LPARAM(lparam), GET_Y_LPARAM(lparam)};
                ScreenToClient(hwnd, &pt);
                if (pt.y >= 0 && pt.y < S(36) &&
                    HitTestButton(pt.x, pt.y) == kButtonNone) {
                    return HTCAPTION;
                }
            }
            return hit;
        }

        case WM_MOUSEMOVE: {
            TRACKMOUSEEVENT track{};
            track.cbSize = sizeof(track);
            track.dwFlags = TME_LEAVE;
            track.hwndTrack = hwnd;
            TrackMouseEvent(&track);

            const int hot = HitTestButton(GET_X_LPARAM(lparam),
                                          GET_Y_LPARAM(lparam));
            if (hot != g_hot_button) {
                g_hot_button = hot;
                InvalidateRect(hwnd, nullptr, FALSE);
            }
            return 0;
        }

        case WM_MOUSELEAVE:
            g_hot_button = kButtonNone;
            InvalidateRect(hwnd, nullptr, FALSE);
            return 0;

        case WM_LBUTTONDOWN: {
            const int id = HitTestButton(GET_X_LPARAM(lparam),
                                         GET_Y_LPARAM(lparam));
            if (id != kButtonNone) {
                g_pressed_button = id;
                SetCapture(hwnd);
                InvalidateRect(hwnd, nullptr, FALSE);
            }
            return 0;
        }

        case WM_LBUTTONUP: {
            const int id = HitTestButton(GET_X_LPARAM(lparam),
                                         GET_Y_LPARAM(lparam));
            const int pressed = g_pressed_button;
            g_pressed_button = kButtonNone;
            if (GetCapture() == hwnd) ReleaseCapture();
            if (pressed != kButtonNone && id == pressed) InvokeButton(id);
            InvalidateRect(hwnd, nullptr, FALSE);
            return 0;
        }

        case kStatusMessage: {
            auto* info = reinterpret_cast<StatusInfo*>(lparam);
            if (info) {
                g_status = std::move(*info);
                delete info;
            }
            UpdateWindowTitle();
            UpdateTrayTip();
            InvalidateRect(hwnd, nullptr, FALSE);
            return 0;
        }

        case kTrayMessage: {
            switch (LOWORD(lparam)) {
                case WM_RBUTTONUP:
                case WM_CONTEXTMENU:
                    ShowTrayMenu();
                    break;
                case WM_LBUTTONDBLCLK:
                    ToggleVisibility();
                    break;
                default:
                    break;
            }
            return 0;
        }

        case WM_TIMER:
            if (wparam == kSelfTestTimer) {
                KillTimer(hwnd, kSelfTestTimer);
                DestroyWindow(hwnd);
            }
            return 0;

        case WM_CLOSE:
            DestroyWindow(hwnd);
            return 0;

        case WM_DESTROY:
            KillTimer(hwnd, kSelfTestTimer);
            if (g_worker) {
                g_worker->Stop();
                g_worker.reset();
            }
            {
                MSG pending{};
                while (PeekMessageW(&pending, hwnd, kStatusMessage,
                                    kStatusMessage, PM_REMOVE)) {
                    delete reinterpret_cast<StatusInfo*>(pending.lParam);
                }
            }
            RemoveTrayIcon();
            if (g_font_title) DeleteObject(g_font_title);
            if (g_font_body) DeleteObject(g_font_body);
            if (g_font_small) DeleteObject(g_font_small);
            if (g_font_button) DeleteObject(g_font_button);
            g_font_title = g_font_body = g_font_small = g_font_button = nullptr;
            g_hwnd = nullptr;
            PostQuitMessage(0);
            return 0;

        default:
            break;
    }
    return DefWindowProcW(hwnd, message, wparam, lparam);
}

int RunOverlay(HINSTANCE instance) {
    g_instance = instance;

    WNDCLASSEXW wc{};
    wc.cbSize = sizeof(wc);
    wc.lpfnWndProc = WndProc;
    wc.hInstance = instance;
    wc.hCursor = LoadCursorW(nullptr, IDC_ARROW);
    wc.lpszClassName = kWindowClass;
    wc.hIcon = LoadIconW(nullptr, IDI_APPLICATION);
    wc.hIconSm = LoadIconW(nullptr, IDI_APPLICATION);
    if (!RegisterClassExW(&wc)) return 3;

    g_show_existing_message = RegisterWindowMessageW(kShowExistingMessage);
    g_taskbar_created_message = RegisterWindowMessageW(L"TaskbarCreated");

    const int system_dpi = [] {
        HDC dc = GetDC(nullptr);
        const int dpi = dc ? GetDeviceCaps(dc, LOGPIXELSY) : 96;
        if (dc) ReleaseDC(nullptr, dc);
        return dpi > 0 ? dpi : 96;
    }();
    const int width = MulDiv(340, system_dpi, 96);
    const int height = MulDiv(150, system_dpi, 96);

    HWND hwnd = CreateWindowExW(
        WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_LAYERED | WS_EX_NOACTIVATE,
        kWindowClass, L"GoPlay Overlay", WS_POPUP, 0, 0, width, height,
        nullptr, nullptr, instance, nullptr);
    if (!hwnd) return 4;

    ShowWindow(hwnd, SW_SHOWNOACTIVATE);
    UpdateWindow(hwnd);

    MSG msg{};
    while (GetMessageW(&msg, nullptr, 0, 0) > 0) {
        TranslateMessage(&msg);
        DispatchMessageW(&msg);
    }
    return 0;
}

}  // namespace

int gp_overlay_run(HINSTANCE instance, const OverlayOptions& options) {
    g_options = options;
    return RunOverlay(instance);
}
