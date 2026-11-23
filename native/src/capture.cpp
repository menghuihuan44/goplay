/*
 * capture.cpp - screen capture for GoPlay.
 *
 * Two backends, selected at gp_capture_init():
 *   1. DXGI Desktop Duplication (fast, GPU-friendly, notifications on change)
 *   2. GDI BitBlt from the desktop DC (works everywhere, including software
 *      rendering / RDP sessions)
 *
 * DXGI is optional: any failure during init or at runtime automatically falls
 * back to GDI.  No process injection, no memory reads, no graphics API hooks.
 */
#define WIN32_LEAN_AND_MEAN
#define NOMINMAX
#include <windows.h>
#include <d3d11.h>
#include <dxgi1_2.h>

#include "goplay_native.h"

#include <cstdlib>
#include <cstring>
#include <cwchar>

namespace {

constexpr int kBackendNone = GP_CAPTURE_BACKEND_NONE;
constexpr int kBackendGdi = GP_CAPTURE_BACKEND_GDI;
constexpr int kBackendDxgi = GP_CAPTURE_BACKEND_DXGI;
constexpr UINT kDxgiAcquireTimeoutMs = 50;

thread_local int tl_gp_error = GP_OK;
thread_local DWORD tl_win32_error = 0;

int fail(int gp_error) {
    tl_gp_error = gp_error;
    if (gp_error != GP_ERR_WIN32) tl_win32_error = 0;
    return gp_error;
}

int fail_win32(DWORD err = GetLastError()) {
    tl_gp_error = GP_ERR_WIN32;
    tl_win32_error = err;
    return GP_ERR_WIN32;
}

struct MonitorRect {
    RECT rc{};
    wchar_t device[32]{};  // \\.\\DISPLAYn, used to pair with a DXGI output
    bool ok = false;
};

// Fixed-size monitor list.  Avoids std::vector so this translation unit does
// not pull the libstdc++ exception allocator (and, through libwinpthread, dead
// OpenProcess/GetThreadContext imports) into the DLL import table.
constexpr size_t kMaxMonitors = 32;

struct MonitorList {
    MonitorRect monitors[kMaxMonitors]{};
    size_t count = 0;
    MonitorRect primary{};
    bool has_primary = false;

    void clear() {
        count = 0;
        has_primary = false;
    }
};

// Growable byte buffer with explicit allocation.  Used for captured frames so
// the capture path stays free of C++ exception machinery.
struct FrameBuffer {
    unsigned char* data = nullptr;
    size_t size = 0;

    FrameBuffer() = default;
    FrameBuffer(const FrameBuffer&) = delete;
    FrameBuffer& operator=(const FrameBuffer&) = delete;
    ~FrameBuffer() { clear(); }

    bool resize(size_t bytes) {
        if (bytes == size) return data != nullptr || bytes == 0;
        unsigned char* grown =
            static_cast<unsigned char*>(std::realloc(data, bytes ? bytes : 1));
        if (!grown) return false;
        data = grown;
        size = bytes;
        return true;
    }

    bool assign(const unsigned char* source, size_t bytes) {
        if (!resize(bytes)) return false;
        if (bytes) std::memcpy(data, source, bytes);
        return true;
    }

    void clear() {
        std::free(data);
        data = nullptr;
        size = 0;
    }
};

BOOL CALLBACK monitor_enum_proc(HMONITOR hmon, HDC, LPRECT, LPARAM user) {
    auto* ctx = reinterpret_cast<MonitorList*>(user);
    if (!ctx || !hmon) return TRUE;
    if (ctx->count >= kMaxMonitors) return FALSE;

    MONITORINFOEXW mi{};
    mi.cbSize = sizeof(mi);
    if (!GetMonitorInfoW(hmon, reinterpret_cast<LPMONITORINFO>(&mi))) {
        return TRUE;
    }

    MonitorRect monitor;
    monitor.rc = mi.rcMonitor;
    monitor.ok = true;
    wcsncpy(monitor.device, mi.szDevice,
            sizeof(monitor.device) / sizeof(monitor.device[0]) - 1);

    ctx->monitors[ctx->count++] = monitor;
    if ((mi.dwFlags & MONITORINFOF_PRIMARY) != 0) {
        ctx->primary = monitor;
        ctx->has_primary = true;
    }
    return TRUE;
}

bool enumerate_monitors(MonitorList& ctx) {
    ctx.clear();
    if (!EnumDisplayMonitors(nullptr, nullptr, monitor_enum_proc,
                             reinterpret_cast<LPARAM>(&ctx))) {
        return false;
    }
    return ctx.count > 0;
}

bool resolve_monitor(int index, MonitorRect& out) {
    if (index < 0) return false;
    MonitorList ctx;
    if (!enumerate_monitors(ctx)) return false;
    if (index == 0) {
        out = ctx.has_primary ? ctx.primary : ctx.monitors[0];
        return true;
    }
    if (index > static_cast<int>(ctx.count)) return false;
    out = ctx.monitors[static_cast<size_t>(index - 1)];
    return true;
}

struct SrwExclusiveLock {
    SRWLOCK* lock = nullptr;

    explicit SrwExclusiveLock(SRWLOCK& value) : lock(&value) {
        AcquireSRWLockExclusive(lock);
    }
    ~SrwExclusiveLock() { ReleaseSRWLockExclusive(lock); }

    SrwExclusiveLock(const SrwExclusiveLock&) = delete;
    SrwExclusiveLock& operator=(const SrwExclusiveLock&) = delete;
};

struct CaptureState {
    SRWLOCK mu = SRWLOCK_INIT;
    bool initialized = false;
    int monitor = 0;
    int backend = kBackendNone;

    // GDI fallback state, lazily (re)built when geometry changes.
    HDC gdi_dc = nullptr;
    HBITMAP gdi_bitmap = nullptr;
    HGDIOBJ gdi_old_bitmap = nullptr;
    void* gdi_bits = nullptr;
    int gdi_w = 0;
    int gdi_h = 0;

    // DXGI Desktop Duplication state.
    ID3D11Device* d3d_device = nullptr;
    ID3D11DeviceContext* d3d_context = nullptr;
    IDXGIOutputDuplication* duplication = nullptr;
    ID3D11Texture2D* staging = nullptr;
    int staging_w = 0;
    int staging_h = 0;
    DXGI_FORMAT staging_format = DXGI_FORMAT_UNKNOWN;

    // Last successfully duplicated frame; Desktop Duplication only produces a
    // frame when the desktop changes, so timeouts return this cached image.
    FrameBuffer cache;
    int cache_w = 0;
    int cache_h = 0;
};

CaptureState g_state;

void release_gdi_locked() {
    if (g_state.gdi_dc) {
        if (g_state.gdi_old_bitmap) {
            SelectObject(g_state.gdi_dc, g_state.gdi_old_bitmap);
        }
        DeleteDC(g_state.gdi_dc);
        g_state.gdi_dc = nullptr;
    }
    g_state.gdi_old_bitmap = nullptr;
    if (g_state.gdi_bitmap) {
        DeleteObject(g_state.gdi_bitmap);
        g_state.gdi_bitmap = nullptr;
    }
    g_state.gdi_bits = nullptr;
    g_state.gdi_w = 0;
    g_state.gdi_h = 0;
}

void release_dxgi_locked() {
    if (g_state.staging) {
        g_state.staging->Release();
        g_state.staging = nullptr;
    }
    g_state.staging_w = 0;
    g_state.staging_h = 0;
    g_state.staging_format = DXGI_FORMAT_UNKNOWN;

    if (g_state.duplication) {
        g_state.duplication->Release();
        g_state.duplication = nullptr;
    }
    if (g_state.d3d_context) {
        g_state.d3d_context->Release();
        g_state.d3d_context = nullptr;
    }
    if (g_state.d3d_device) {
        g_state.d3d_device->Release();
        g_state.d3d_device = nullptr;
    }
    g_state.cache.clear();
    g_state.cache_w = 0;
    g_state.cache_h = 0;
}

void release_all_locked() {
    release_dxgi_locked();
    release_gdi_locked();
}

bool ensure_gdi_locked(int width, int height) {
    if (g_state.gdi_dc && g_state.gdi_bitmap && g_state.gdi_bits &&
        g_state.gdi_w == width && g_state.gdi_h == height) {
        return true;
    }
    release_gdi_locked();

    HDC screen = GetDC(nullptr);
    if (!screen) return false;

    BITMAPINFO bi{};
    bi.bmiHeader.biSize = sizeof(BITMAPINFOHEADER);
    bi.bmiHeader.biWidth = width;
    bi.bmiHeader.biHeight = -height;  // top-down rows
    bi.bmiHeader.biPlanes = 1;
    bi.bmiHeader.biBitCount = 32;
    bi.bmiHeader.biCompression = BI_RGB;

    HDC mem = CreateCompatibleDC(screen);
    void* bits = nullptr;
    HBITMAP dib = CreateDIBSection(screen, &bi, DIB_RGB_COLORS, &bits, nullptr, 0);
    ReleaseDC(nullptr, screen);

    if (!mem || !dib || !bits) {
        if (mem) DeleteDC(mem);
        if (dib) DeleteObject(dib);
        return false;
    }

    g_state.gdi_old_bitmap = SelectObject(mem, dib);
    g_state.gdi_dc = mem;
    g_state.gdi_bitmap = dib;
    g_state.gdi_bits = bits;
    g_state.gdi_w = width;
    g_state.gdi_h = height;
    return true;
}

// Returns false and fills *err on failure.
bool capture_gdi_locked(int& width, int& height, FrameBuffer& out, int& err) {
    MonitorRect monitor;
    if (!resolve_monitor(g_state.monitor, monitor)) {
        err = fail(GP_ERR_INVALID_ARG);
        return false;
    }
    const RECT& rc = monitor.rc;
    const int mw = rc.right - rc.left;
    const int mh = rc.bottom - rc.top;
    if (mw <= 0 || mh <= 0) {
        err = fail(GP_ERR_INVALID_ARG);
        return false;
    }
    if (!ensure_gdi_locked(mw, mh)) {
        err = fail_win32();
        return false;
    }

    HDC screen = GetDC(nullptr);
    if (!screen) {
        err = fail_win32();
        return false;
    }
    // A desktop mode change (which DXGI probing/release can trigger) makes the
    // first BitBlt fail with ERROR_INVALID_HANDLE for a few milliseconds.
    BOOL ok = FALSE;
    DWORD bitblt_error = ERROR_SUCCESS;
    for (int attempt = 0; attempt < 4; ++attempt) {
        if (attempt > 0) Sleep(15);
        SetLastError(ERROR_SUCCESS);
        ok = BitBlt(g_state.gdi_dc, 0, 0, mw, mh, screen, rc.left, rc.top,
                    SRCCOPY);
        if (ok) break;
        bitblt_error = GetLastError();
    }
    ReleaseDC(nullptr, screen);
    if (!ok) {
        err = fail_win32(bitblt_error ? bitblt_error : ERROR_GEN_FAILURE);
        return false;
    }

    const size_t bytes = static_cast<size_t>(mw) * static_cast<size_t>(mh) * 4u;
    if (!out.resize(bytes)) {
        err = fail(GP_ERR_NO_MEMORY);
        return false;
    }
    std::memcpy(out.data, g_state.gdi_bits, bytes);
    width = mw;
    height = mh;
    return true;
}

bool init_dxgi_locked(const MonitorRect& monitor) {
    const RECT& rc = monitor.rc;
    D3D_FEATURE_LEVEL levels[] = {D3D_FEATURE_LEVEL_11_0,
                                  D3D_FEATURE_LEVEL_10_1,
                                  D3D_FEATURE_LEVEL_10_0};
    D3D_FEATURE_LEVEL got = D3D_FEATURE_LEVEL_11_0;
    const UINT flags = D3D11_CREATE_DEVICE_BGRA_SUPPORT;
    HRESULT hr = D3D11CreateDevice(nullptr, D3D_DRIVER_TYPE_HARDWARE, nullptr,
                                   flags, levels,
                                   static_cast<UINT>(sizeof(levels) / sizeof(levels[0])),
                                   D3D11_SDK_VERSION, &g_state.d3d_device, &got,
                                   &g_state.d3d_context);
    if (FAILED(hr) || !g_state.d3d_device || !g_state.d3d_context) {
        release_dxgi_locked();
        return false;
    }

    IDXGIDevice* dxgi_device = nullptr;
    hr = g_state.d3d_device->QueryInterface(__uuidof(IDXGIDevice),
                                            reinterpret_cast<void**>(&dxgi_device));
    if (FAILED(hr) || !dxgi_device) {
        release_dxgi_locked();
        return false;
    }

    IDXGIAdapter* adapter = nullptr;
    hr = dxgi_device->GetAdapter(&adapter);
    dxgi_device->Release();
    if (FAILED(hr) || !adapter) {
        release_dxgi_locked();
        return false;
    }

    // Pair the monitor with its DXGI output by device name first.  The
    // DesktopCoordinates rectangle can be DPI-virtualized differently from
    // EnumDisplayMonitors(), so it is only a fallback.
    IDXGIOutput* target = nullptr;
    IDXGIOutput* rect_fallback = nullptr;
    for (UINT i = 0;; ++i) {
        IDXGIOutput* output = nullptr;
        hr = adapter->EnumOutputs(i, &output);
        if (hr == DXGI_ERROR_NOT_FOUND || FAILED(hr) || !output) break;

        DXGI_OUTPUT_DESC desc{};
        if (SUCCEEDED(output->GetDesc(&desc)) && desc.AttachedToDesktop) {
            const bool name_match =
                monitor.device[0] != L'\0' &&
                std::wcscmp(monitor.device, desc.DeviceName) == 0;
            const RECT& d = desc.DesktopCoordinates;
            const bool rect_match =
                d.left == rc.left && d.top == rc.top &&
                (d.right - d.left) == (rc.right - rc.left) &&
                (d.bottom - d.top) == (rc.bottom - rc.top);
            if (name_match) {
                target = output;
                break;
            }
            if (rect_match && !rect_fallback) {
                rect_fallback = output;
                continue;
            }
        }
        output->Release();
    }
    if (!target) {
        target = rect_fallback;
        rect_fallback = nullptr;
    }
    if (rect_fallback) rect_fallback->Release();
    adapter->Release();
    if (!target) {
        release_dxgi_locked();
        return false;
    }

    IDXGIOutput1* output1 = nullptr;
    hr = target->QueryInterface(__uuidof(IDXGIOutput1),
                                reinterpret_cast<void**>(&output1));
    target->Release();
    if (FAILED(hr) || !output1) {
        release_dxgi_locked();
        return false;
    }

    hr = output1->DuplicateOutput(g_state.d3d_device, &g_state.duplication);
    output1->Release();
    if (FAILED(hr) || !g_state.duplication) {
        release_dxgi_locked();
        return false;
    }

    DXGI_OUTDUPL_DESC dup_desc{};
    g_state.duplication->GetDesc(&dup_desc);
    const DXGI_FORMAT fmt = dup_desc.ModeDesc.Format;
    // Desired output is BGRA. HDR (FP16) or 10-bit surfaces are uncommon for
    // Desktop Duplication; use the GDI fallback rather than approximate them.
    if (fmt != DXGI_FORMAT_B8G8R8A8_UNORM &&
        fmt != DXGI_FORMAT_B8G8R8A8_UNORM_SRGB) {
        release_dxgi_locked();
        return false;
    }

    g_state.cache.clear();
    g_state.cache_w = 0;
    g_state.cache_h = 0;
    return true;
}

bool ensure_staging_locked(const D3D11_TEXTURE2D_DESC& src) {
    if (g_state.staging && g_state.staging_w == static_cast<int>(src.Width) &&
        g_state.staging_h == static_cast<int>(src.Height) &&
        g_state.staging_format == src.Format) {
        return true;
    }
    if (g_state.staging) {
        g_state.staging->Release();
        g_state.staging = nullptr;
    }
    g_state.staging_w = 0;
    g_state.staging_h = 0;
    g_state.staging_format = DXGI_FORMAT_UNKNOWN;

    D3D11_TEXTURE2D_DESC desc = src;
    desc.Usage = D3D11_USAGE_STAGING;
    desc.BindFlags = 0;
    desc.CPUAccessFlags = D3D11_CPU_ACCESS_READ;
    desc.MiscFlags = 0;
    desc.ArraySize = 1;
    desc.MipLevels = 1;
    desc.SampleDesc.Count = 1;
    desc.SampleDesc.Quality = 0;

    HRESULT hr = g_state.d3d_device->CreateTexture2D(&desc, nullptr, &g_state.staging);
    if (FAILED(hr) || !g_state.staging) {
        g_state.staging = nullptr;
        return false;
    }
    g_state.staging_w = static_cast<int>(src.Width);
    g_state.staging_h = static_cast<int>(src.Height);
    g_state.staging_format = src.Format;
    return true;
}

// Returns false with err == GP_ERR_TIMEOUT when the desktop did not change yet;
// the caller may then keep DXGI enabled and use GDI for this single frame.
bool capture_dxgi_locked(int& width, int& height, FrameBuffer& out, int& err) {
    if (!g_state.duplication) {
        err = fail(GP_ERR_UNSUPPORTED);
        return false;
    }

    DXGI_OUTDUPL_FRAME_INFO frame_info{};
    IDXGIResource* resource = nullptr;
    HRESULT hr = g_state.duplication->AcquireNextFrame(kDxgiAcquireTimeoutMs,
                                                       &frame_info, &resource);

    if (hr == DXGI_ERROR_WAIT_TIMEOUT) {
        if (g_state.cache.size == 0) {
            err = fail(GP_ERR_TIMEOUT);
            return false;
        }
        width = g_state.cache_w;
        height = g_state.cache_h;
        if (!out.assign(g_state.cache.data, g_state.cache.size)) {
            err = fail(GP_ERR_NO_MEMORY);
            return false;
        }
        return true;
    }
    if (FAILED(hr) || !resource) {
        // ACCESS_LOST happens on resolution/mode changes or when another
        // duplicator takes over. The caller falls back to GDI permanently.
        err = fail(hr == DXGI_ERROR_ACCESS_LOST ? GP_ERR_UNSUPPORTED : GP_ERR_WIN32);
        if (hr != DXGI_ERROR_ACCESS_LOST) tl_win32_error = static_cast<DWORD>(hr);
        if (resource) resource->Release();
        return false;
    }

    ID3D11Texture2D* texture = nullptr;
    hr = resource->QueryInterface(__uuidof(ID3D11Texture2D),
                                  reinterpret_cast<void**>(&texture));
    resource->Release();
    if (FAILED(hr) || !texture) {
        g_state.duplication->ReleaseFrame();
        err = fail(GP_ERR_UNSUPPORTED);
        return false;
    }

    D3D11_TEXTURE2D_DESC desc{};
    texture->GetDesc(&desc);
    const bool format_ok = desc.Format == DXGI_FORMAT_B8G8R8A8_UNORM ||
                           desc.Format == DXGI_FORMAT_B8G8R8A8_UNORM_SRGB;
    if (!format_ok) {
        texture->Release();
        g_state.duplication->ReleaseFrame();
        // Surface format changed (e.g. HDR enabled): abandon DXGI.
        release_dxgi_locked();
        err = fail(GP_ERR_UNSUPPORTED);
        return false;
    }

    bool ok = false;
    if (ensure_staging_locked(desc)) {
        g_state.d3d_context->CopyResource(g_state.staging, texture);
        D3D11_MAPPED_SUBRESOURCE mapped{};
        hr = g_state.d3d_context->Map(g_state.staging, 0, D3D11_MAP_READ, 0, &mapped);
        if (SUCCEEDED(hr)) {
            const int mw = static_cast<int>(desc.Width);
            const int mh = static_cast<int>(desc.Height);
            const size_t row = static_cast<size_t>(mw) * 4u;
            if (out.resize(row * static_cast<size_t>(mh))) {
                for (int y = 0; y < mh; ++y) {
                    std::memcpy(out.data + static_cast<size_t>(y) * row,
                                static_cast<const unsigned char*>(mapped.pData) +
                                    static_cast<size_t>(y) * mapped.RowPitch,
                                row);
                }
                g_state.d3d_context->Unmap(g_state.staging, 0);
                width = mw;
                height = mh;
                if (g_state.cache.assign(out.data, out.size)) {
                    g_state.cache_w = mw;
                    g_state.cache_h = mh;
                    ok = true;
                }
            } else {
                g_state.d3d_context->Unmap(g_state.staging, 0);
            }
        }
    }

    texture->Release();
    g_state.duplication->ReleaseFrame();
    if (!ok) {
        err = fail_win32(static_cast<DWORD>(hr));
        return false;
    }
    return true;
}

void reset_frame_outputs(unsigned char** data, int* width, int* height,
                         int* stride) {
    if (data) *data = nullptr;
    if (width) *width = 0;
    if (height) *height = 0;
    if (stride) *stride = 0;
}

}  // namespace

extern "C" {

GP_API int gp_capture_init(int monitor) {
    if (monitor < 0) return fail(GP_ERR_INVALID_ARG);

    SrwExclusiveLock lock(g_state.mu);
    release_all_locked();
    g_state.initialized = false;
    g_state.backend = kBackendNone;

    MonitorRect resolved;
    if (!resolve_monitor(monitor, resolved)) return fail(GP_ERR_INVALID_ARG);
    if (resolved.rc.right <= resolved.rc.left ||
        resolved.rc.bottom <= resolved.rc.top) {
        return fail(GP_ERR_INVALID_ARG);
    }

    g_state.monitor = monitor;
    g_state.initialized = true;
    g_state.backend = kBackendGdi;

    // Allow tests / troubleshooting to force the GDI path.
    wchar_t disable[8]{};
    const DWORD n = GetEnvironmentVariableW(L"GP_NATIVE_DISABLE_DXGI", disable,
                                            static_cast<DWORD>(sizeof(disable) / sizeof(disable[0])));
    const bool dxgi_allowed = !(n > 0 && disable[0] != L'\0' && disable[0] != L'0');

    if (dxgi_allowed && init_dxgi_locked(resolved)) {
        g_state.backend = kBackendDxgi;
    }

    tl_gp_error = GP_OK;
    tl_win32_error = 0;
    return GP_OK;
}

GP_API int gp_capture_frame(unsigned char** data, int* width, int* height,
                            int* stride) {
    if (!data || !width || !height || !stride) return fail(GP_ERR_INVALID_ARG);
    reset_frame_outputs(data, width, height, stride);

    SrwExclusiveLock lock(g_state.mu);
    if (!g_state.initialized) return fail(GP_ERR_NOT_INITIALIZED);

    FrameBuffer frame;
    int fw = 0;
    int fh = 0;
    int err = GP_ERR_UNSUPPORTED;
    bool ok = false;

    if (g_state.backend == kBackendDxgi) {
        ok = capture_dxgi_locked(fw, fh, frame, err);
        if (!ok && err != GP_ERR_TIMEOUT) {
            // Runtime DXGI failure: release it and use GDI from now on.
            release_dxgi_locked();
            g_state.backend = kBackendGdi;
        }
    }
    if (!ok) {
        ok = capture_gdi_locked(fw, fh, frame, err);
    }
    if (!ok) {
        tl_gp_error = err;
        return err;
    }

    if (fw <= 0 || fh <= 0 || frame.size != static_cast<size_t>(fw) *
                                                    static_cast<size_t>(fh) * 4u) {
        return fail(GP_ERR_UNSUPPORTED);
    }

    unsigned char* buffer = static_cast<unsigned char*>(std::malloc(frame.size));
    if (!buffer) return fail(GP_ERR_NO_MEMORY);
    std::memcpy(buffer, frame.data, frame.size);

    *data = buffer;
    *width = fw;
    *height = fh;
    *stride = fw * 4;
    tl_gp_error = GP_OK;
    tl_win32_error = 0;
    return GP_OK;
}

GP_API void gp_capture_free(unsigned char* data) {
    std::free(data);
}

GP_API void gp_capture_close(void) {
    SrwExclusiveLock lock(g_state.mu);
    release_all_locked();
    g_state.initialized = false;
    g_state.backend = kBackendNone;
}

GP_API int gp_capture_backend(void) {
    SrwExclusiveLock lock(g_state.mu);
    return g_state.initialized ? g_state.backend : GP_CAPTURE_BACKEND_NONE;
}

GP_API int gp_capture_monitor_count(void) {
    MonitorList ctx;
    if (!enumerate_monitors(ctx)) return 0;
    return static_cast<int>(ctx.count);
}

GP_API const char* gp_capture_backend_name(void) {
    const int backend = gp_capture_backend();
    if (backend == GP_CAPTURE_BACKEND_DXGI) return "dxgi";
    if (backend == GP_CAPTURE_BACKEND_GDI) return "gdi";
    return "none";
}

GP_API int gp_capture_last_error(void) {
    return static_cast<int>(tl_win32_error);
}

}  // extern "C"
