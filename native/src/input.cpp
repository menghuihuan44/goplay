/*
 * input.cpp - user-level keyboard / mouse synthesis for GoPlay.
 *
 * Every event is injected with SendInput().  This file deliberately contains no
 * OpenProcess/ReadProcessMemory/WriteProcessMemory/CreateRemoteThread/
 * SetWindowsHookEx usage (see native/README.md security audit).
 */
#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>

#include "goplay_native.h"

#include <cstdlib>

namespace {

constexpr size_t kMaxTextUnits = 1u << 20;  // 1M UTF-16 code units
constexpr size_t kMaxBatchInputs = 512;

// Fixed-size SendInput batch.  std::vector would instantiate libstdc++'s
// __throw_length_error path, which drags libwinpthread (and its unused
// OpenProcess/GetThreadContext imports) into the DLL import table.
struct InputBatch {
    INPUT items[kMaxBatchInputs]{};
    size_t count = 0;

    bool empty() const { return count == 0; }
    void clear() { count = 0; }
    bool push(const INPUT& input) {
        if (count >= kMaxBatchInputs) return false;
        items[count++] = input;
        return true;
    }
};

thread_local DWORD tl_win32_error = 0;

int fail_win32(DWORD err = GetLastError()) {
    tl_win32_error = err;
    return GP_ERR_WIN32;
}

int fail_invalid() {
    tl_win32_error = ERROR_INVALID_PARAMETER;
    return GP_ERR_INVALID_ARG;
}

int ok_result() {
    tl_win32_error = 0;
    return GP_OK;
}

bool valid_button(int button) {
    return button >= GP_MOUSE_LEFT && button <= GP_MOUSE_X2;
}

DWORD mouse_down_flag(int button) {
    switch (button) {
        case GP_MOUSE_LEFT: return MOUSEEVENTF_LEFTDOWN;
        case GP_MOUSE_RIGHT: return MOUSEEVENTF_RIGHTDOWN;
        case GP_MOUSE_MIDDLE: return MOUSEEVENTF_MIDDLEDOWN;
        case GP_MOUSE_X1:
        case GP_MOUSE_X2: return MOUSEEVENTF_XDOWN;
        default: return 0;
    }
}

DWORD mouse_up_flag(int button) {
    switch (button) {
        case GP_MOUSE_LEFT: return MOUSEEVENTF_LEFTUP;
        case GP_MOUSE_RIGHT: return MOUSEEVENTF_RIGHTUP;
        case GP_MOUSE_MIDDLE: return MOUSEEVENTF_MIDDLEUP;
        case GP_MOUSE_X1:
        case GP_MOUSE_X2: return MOUSEEVENTF_XUP;
        default: return 0;
    }
}

DWORD xbutton_data(int button) {
    return button == GP_MOUSE_X1 ? XBUTTON1 : XBUTTON2;
}

bool send_inputs(const INPUT* inputs, size_t count) {
    if (count == 0) return true;
    UINT sent = 0;
    if (count <= static_cast<size_t>(UINT_MAX)) {
        sent = SendInput(static_cast<UINT>(count),
                         const_cast<INPUT*>(inputs), sizeof(INPUT));
    }
    if (sent != count) {
        fail_win32();
        return false;
    }
    return true;
}

bool send_one(const INPUT& input) {
    return send_inputs(&input, 1);
}

LONG normalized_axis(int value, int origin, int extent) {
    if (extent <= 1) return 0;
    const long long rel = static_cast<long long>(value) - origin;
    long long scaled = rel * 65535LL / (extent - 1);
    if (scaled < 0) scaled = 0;
    if (scaled > 65535) scaled = 65535;
    return static_cast<LONG>(scaled);
}

bool virtual_desktop(int& left, int& top, int& width, int& height) {
    left = GetSystemMetrics(SM_XVIRTUALSCREEN);
    top = GetSystemMetrics(SM_YVIRTUALSCREEN);
    width = GetSystemMetrics(SM_CXVIRTUALSCREEN);
    height = GetSystemMetrics(SM_CYVIRTUALSCREEN);
    return width > 1 && height > 1;
}

int move_absolute(int x, int y) {
    int vx = 0, vy = 0, vw = 0, vh = 0;
    if (!virtual_desktop(vx, vy, vw, vh)) return fail_win32();

    INPUT in{};
    in.type = INPUT_MOUSE;
    in.mi.dx = normalized_axis(x, vx, vw);
    in.mi.dy = normalized_axis(y, vy, vh);
    in.mi.dwFlags = MOUSEEVENTF_MOVE | MOUSEEVENTF_ABSOLUTE |
                    MOUSEEVENTF_VIRTUALDESK;
    return send_one(in) ? ok_result() : GP_ERR_WIN32;
}

bool is_extended_key(int vk) {
    switch (vk) {
        case VK_LEFT:
        case VK_RIGHT:
        case VK_UP:
        case VK_DOWN:
        case VK_HOME:
        case VK_END:
        case VK_PRIOR:
        case VK_NEXT:
        case VK_INSERT:
        case VK_DELETE:
        case VK_RCONTROL:
        case VK_RMENU:
        case VK_NUMLOCK:
        case VK_DIVIDE:
        case VK_SNAPSHOT:
            return true;
        default:
            return false;
    }
}

bool key_event(int vk, bool down) {
    INPUT in{};
    in.type = INPUT_KEYBOARD;
    in.ki.wVk = static_cast<WORD>(vk);
    in.ki.wScan = static_cast<WORD>(MapVirtualKeyW(static_cast<UINT>(vk),
                                                   MAPVK_VK_TO_VSC));
    in.ki.dwFlags = down ? 0 : KEYEVENTF_KEYUP;
    if (is_extended_key(vk)) in.ki.dwFlags |= KEYEVENTF_EXTENDEDKEY;
    return send_one(in);
}

bool scan_event(int scan, bool extended, bool down) {
    INPUT in{};
    in.type = INPUT_KEYBOARD;
    in.ki.wVk = 0;
    in.ki.wScan = static_cast<WORD>(scan & 0xFF);
    in.ki.dwFlags = KEYEVENTF_SCANCODE;
    if (extended) in.ki.dwFlags |= KEYEVENTF_EXTENDEDKEY;
    if (!down) in.ki.dwFlags |= KEYEVENTF_KEYUP;
    return send_one(in);
}

INPUT unicode_event(wchar_t unit, bool down) {
    INPUT in{};
    in.type = INPUT_KEYBOARD;
    in.ki.wVk = 0;
    in.ki.wScan = static_cast<WORD>(unit);
    in.ki.dwFlags = KEYEVENTF_UNICODE;
    if (!down) in.ki.dwFlags |= KEYEVENTF_KEYUP;
    return in;
}

int type_wide(const wchar_t* text) {
    if (!text) return fail_invalid();

    size_t length = 0;
    while (length <= kMaxTextUnits && text[length] != L'\0') ++length;
    if (length > kMaxTextUnits) {
        tl_win32_error = 0;
        return GP_ERR_TOO_LARGE;
    }
    if (length == 0) return ok_result();

    InputBatch batch;
    for (size_t i = 0; i < length; ++i) {
        const wchar_t ch = text[i];
        if (ch == L'\r') continue;
        if (ch == L'\n') {
            if (!batch.empty()) {
                if (!send_inputs(batch.items, batch.count)) return GP_ERR_WIN32;
                batch.clear();
            }
            if (!key_event(VK_RETURN, true)) return GP_ERR_WIN32;
            if (!key_event(VK_RETURN, false)) return GP_ERR_WIN32;
            continue;
        }
        if (batch.count + 2 > kMaxBatchInputs) {
            if (!send_inputs(batch.items, batch.count)) return GP_ERR_WIN32;
            batch.clear();
        }
        batch.push(unicode_event(ch, true));
        batch.push(unicode_event(ch, false));
    }
    if (!batch.empty() && !send_inputs(batch.items, batch.count)) {
        return GP_ERR_WIN32;
    }
    return ok_result();
}

// Decodes into caller-provided storage.  capacity must be at least
// utf8_length + 1; every UTF-8 sequence consumes at least as many bytes as the
// UTF-16 units it produces.
bool utf8_to_wide(const char* text, wchar_t* out, size_t capacity,
                  size_t& out_length, int& error) {
    error = GP_OK;
    size_t length = 0;
    while (length <= kMaxTextUnits && text[length] != '\0') ++length;
    if (length > kMaxTextUnits) {
        error = GP_ERR_TOO_LARGE;
        return false;
    }
    if (length + 1 > capacity) {
        error = GP_ERR_TOO_LARGE;
        return false;
    }

    out_length = 0;
    const auto* s = reinterpret_cast<const unsigned char*>(text);
    size_t i = 0;
    while (i < length) {
        const unsigned char b0 = s[i];
        unsigned int cp = 0;
        size_t extra = 0;
        if (b0 < 0x80) {
            cp = b0;
        } else if (b0 >= 0xC2 && b0 <= 0xDF) {
            cp = b0 & 0x1F;
            extra = 1;
        } else if (b0 >= 0xE0 && b0 <= 0xEF) {
            cp = b0 & 0x0F;
            extra = 2;
        } else if (b0 >= 0xF0 && b0 <= 0xF4) {
            cp = b0 & 0x07;
            extra = 3;
        } else {
            error = GP_ERR_UTF8;
            return false;
        }

        if (i + extra >= length) {
            error = GP_ERR_UTF8;
            return false;
        }
        for (size_t k = 1; k <= extra; ++k) {
            const unsigned char bx = s[i + k];
            if ((bx & 0xC0) != 0x80) {
                error = GP_ERR_UTF8;
                return false;
            }
            cp = (cp << 6) | (bx & 0x3F);
        }

        if ((extra == 2 && cp < 0x800) || (extra == 3 && cp < 0x10000) ||
            cp > 0x10FFFF || (cp >= 0xD800 && cp <= 0xDFFF)) {
            error = GP_ERR_UTF8;
            return false;
        }

        if (cp <= 0xFFFF) {
            out[out_length++] = static_cast<wchar_t>(cp);
        } else {
            cp -= 0x10000;
            out[out_length++] = static_cast<wchar_t>(0xD800 + (cp >> 10));
            out[out_length++] = static_cast<wchar_t>(0xDC00 + (cp & 0x3FF));
        }
        i += extra + 1;
    }
    out[out_length] = L'\0';
    return true;
}

}  // namespace

extern "C" {

GP_API int gp_input_init(void) {
    return ok_result();
}

GP_API int gp_input_move(int x, int y) {
    return move_absolute(x, y) == GP_OK ? GP_OK : GP_ERR_WIN32;
}

GP_API int gp_input_move_rel(int dx, int dy) {
    INPUT in{};
    in.type = INPUT_MOUSE;
    in.mi.dx = dx;
    in.mi.dy = dy;
    in.mi.dwFlags = MOUSEEVENTF_MOVE;
    return send_one(in) ? ok_result() : GP_ERR_WIN32;
}

GP_API int gp_input_click(int x, int y, int button, int count) {
    if (!valid_button(button)) return fail_invalid();
    if ((x < 0) != (y < 0)) return fail_invalid();
    if (count < 1) count = 1;
    if (count > 20) count = 20;

    if (x >= 0 && y >= 0 && gp_input_move(x, y) != GP_OK) return GP_ERR_WIN32;

    for (int i = 0; i < count; ++i) {
        INPUT down{};
        down.type = INPUT_MOUSE;
        down.mi.dwFlags = mouse_down_flag(button);
        if (button == GP_MOUSE_X1 || button == GP_MOUSE_X2) {
            down.mi.mouseData = xbutton_data(button);
        }
        if (!send_one(down)) return GP_ERR_WIN32;

        Sleep(20);

        INPUT up{};
        up.type = INPUT_MOUSE;
        up.mi.dwFlags = mouse_up_flag(button);
        if (button == GP_MOUSE_X1 || button == GP_MOUSE_X2) {
            up.mi.mouseData = xbutton_data(button);
        }
        if (!send_one(up)) return GP_ERR_WIN32;

        if (i + 1 < count) Sleep(40);
    }
    return ok_result();
}

GP_API int gp_input_down(int button) {
    if (!valid_button(button)) return fail_invalid();
    INPUT in{};
    in.type = INPUT_MOUSE;
    in.mi.dwFlags = mouse_down_flag(button);
    if (button == GP_MOUSE_X1 || button == GP_MOUSE_X2) {
        in.mi.mouseData = xbutton_data(button);
    }
    return send_one(in) ? ok_result() : GP_ERR_WIN32;
}

GP_API int gp_input_up(int button) {
    if (!valid_button(button)) return fail_invalid();
    INPUT in{};
    in.type = INPUT_MOUSE;
    in.mi.dwFlags = mouse_up_flag(button);
    if (button == GP_MOUSE_X1 || button == GP_MOUSE_X2) {
        in.mi.mouseData = xbutton_data(button);
    }
    return send_one(in) ? ok_result() : GP_ERR_WIN32;
}

GP_API int gp_input_key_down(int vk) {
    if (vk <= 0 || vk > 254) return fail_invalid();
    return key_event(vk, true) ? ok_result() : GP_ERR_WIN32;
}

GP_API int gp_input_key_up(int vk) {
    if (vk <= 0 || vk > 254) return fail_invalid();
    return key_event(vk, false) ? ok_result() : GP_ERR_WIN32;
}

GP_API int gp_input_key_press(int vk, int hold_ms) {
    if (vk <= 0 || vk > 254) return fail_invalid();
    if (hold_ms < 0) hold_ms = 0;
    if (hold_ms > 10000) hold_ms = 10000;
    if (!key_event(vk, true)) return GP_ERR_WIN32;
    if (hold_ms > 0) Sleep(static_cast<DWORD>(hold_ms));
    if (!key_event(vk, false)) return GP_ERR_WIN32;
    return ok_result();
}

GP_API int gp_input_key_scan(int scan, int extended, int down) {
    if (scan < 0 || scan > 0xFF) return fail_invalid();
    return scan_event(scan, extended != 0, down != 0) ? ok_result()
                                                      : GP_ERR_WIN32;
}

GP_API int gp_input_text(const wchar_t* text) {
    return type_wide(text);
}

GP_API int gp_input_text_utf8(const char* text) {
    if (!text) return fail_invalid();
    size_t length = 0;
    while (length <= kMaxTextUnits && text[length] != '\0') ++length;
    if (length > kMaxTextUnits) {
        tl_win32_error = 0;
        return GP_ERR_TOO_LARGE;
    }

    wchar_t* wide =
        static_cast<wchar_t*>(std::malloc((length + 1) * sizeof(wchar_t)));
    if (!wide) {
        tl_win32_error = 0;
        return GP_ERR_NO_MEMORY;
    }

    size_t units = 0;
    int error = GP_OK;
    const bool decoded = utf8_to_wide(text, wide, length + 1, units, error);
    const int result = decoded ? type_wide(wide) : error;
    std::free(wide);
    if (!decoded) tl_win32_error = 0;
    return result;
}

GP_API int gp_input_wheel(int delta) {
    if (delta == 0) return ok_result();
    if (delta < -12000) delta = -12000;
    if (delta > 12000) delta = 12000;

    INPUT in{};
    in.type = INPUT_MOUSE;
    in.mi.dwFlags = MOUSEEVENTF_WHEEL;
    in.mi.mouseData = static_cast<DWORD>(delta);
    return send_one(in) ? ok_result() : GP_ERR_WIN32;
}

GP_API int gp_input_last_error(void) {
    return static_cast<int>(tl_win32_error);
}

}  // extern "C"
