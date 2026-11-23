/*
 * goplay_native.h - stable C ABI for the GoPlay Windows native helper.
 *
 * The DLL (goplay_native.dll) only performs user-level, documented Win32
 * operations:
 *   - GDI BitBlt screen capture and, when available, DXGI Desktop Duplication
 *   - SendInput keyboard / mouse / text synthesis
 *
 * It MUST NOT be extended with process injection, memory reading, graphics API
 * hooking or kernel drivers. See native/README.md ("Security audit").
 *
 * ABI notes:
 *   - x86-64 Windows, __cdecl, plain C linkage.
 *   - All functions are thread-safe unless noted otherwise.
 *   - Every successful gp_capture_frame() returns a malloc()ed buffer owned by
 *     the caller; release it with gp_capture_free(). Buffers are never shared
 *     between calls.
 *   - Error codes are negative GP_ERR_* values; 0 is success.
 */
#ifndef GOPLAY_NATIVE_H
#define GOPLAY_NATIVE_H

#include <stddef.h>

#if defined(_WIN32)
#  if defined(GP_NATIVE_BUILD)
#    define GP_API __declspec(dllexport)
#  else
#    define GP_API __declspec(dllimport)
#  endif
#else
#  define GP_API
#endif

#ifdef __cplusplus
extern "C" {
#endif

/* ---- error codes ------------------------------------------------------- */
#define GP_OK                  0
#define GP_ERR_INVALID_ARG    -1   /* bad argument / monitor out of range   */
#define GP_ERR_NOT_INITIALIZED -2  /* gp_capture_init() not called          */
#define GP_ERR_WIN32          -3   /* a Win32 call failed                   */
#define GP_ERR_UNSUPPORTED    -4   /* feature unavailable on this system    */
#define GP_ERR_NO_MEMORY      -5   /* allocation failed                     */
#define GP_ERR_TIMEOUT        -6   /* no frame available before deadline    */
#define GP_ERR_UTF8           -7   /* malformed UTF-8 input                 */
#define GP_ERR_TOO_LARGE      -8   /* input exceeds hard limit              */

/* ---- capture ----------------------------------------------------------- */
/*
 * gp_capture_init(monitor):
 *   monitor == 0 selects the primary monitor; 1..N select monitors in
 *   EnumDisplayMonitors() order. Tries DXGI Desktop Duplication first and
 *   silently falls back to GDI BitBlt when DXGI is unavailable.
 *   Returns GP_OK or a negative GP_ERR_* code.
 */
GP_API int  gp_capture_init(int monitor);

/*
 * gp_capture_frame(data, width, height, stride):
 *   Produces one top-down BGRA frame (32 bpp, stride == width * 4).
 *   *data is malloc()ed and must be freed with gp_capture_free().
 *   On failure *data is NULL and width/height/stride are set to 0; inspect
 *   gp_capture_last_error() for the underlying Win32/HRESULT value.
 */
GP_API int  gp_capture_frame(unsigned char** data, int* width, int* height,
                             int* stride);

/* Free a buffer returned by gp_capture_frame(). NULL is ignored. */
GP_API void gp_capture_free(unsigned char* data);

/* Release all capture resources. Safe to call repeatedly. */
GP_API void gp_capture_close(void);

/* GP_CAPTURE_BACKEND_* currently in use; 0 when not initialized. */
#define GP_CAPTURE_BACKEND_NONE 0
#define GP_CAPTURE_BACKEND_GDI  1
#define GP_CAPTURE_BACKEND_DXGI 2
GP_API int  gp_capture_backend(void);

/* Number of active monitors as seen by EnumDisplayMonitors(). */
GP_API int  gp_capture_monitor_count(void);

/* Human-readable static name of the backend ("gdi", "dxgi" or "none"). */
GP_API const char* gp_capture_backend_name(void);

/* Win32 GetLastError() value from the most recent failed call on this thread. */
GP_API int  gp_capture_last_error(void);

/* ---- input ------------------------------------------------------------- */
/* Mouse buttons accepted by gp_input_*: 0=left 1=right 2=middle 3=X1 4=X2 */
#define GP_MOUSE_LEFT   0
#define GP_MOUSE_RIGHT  1
#define GP_MOUSE_MIDDLE 2
#define GP_MOUSE_X1     3
#define GP_MOUSE_X2     4

/* Optional, currently only validates the environment; returns GP_OK. */
GP_API int  gp_input_init(void);

/* Absolute move in physical screen coordinates (virtual desktop aware). */
GP_API int  gp_input_move(int x, int y);

/* Relative move; clamped to a sane range by SendInput itself. */
GP_API int  gp_input_move_rel(int dx, int dy);

/* Move to (x,y) (pass -1,-1 to keep the current position) then click.
 * count is clamped to 1..20. Holds the button for ~20 ms. */
GP_API int  gp_input_click(int x, int y, int button, int count);

/* Button down/up without moving. */
GP_API int  gp_input_down(int button);
GP_API int  gp_input_up(int button);

/* Virtual-key input (VK_*). hold_ms is clamped to 0..10000. */
GP_API int  gp_input_key_down(int vk);
GP_API int  gp_input_key_up(int vk);
GP_API int  gp_input_key_press(int vk, int hold_ms);

/*
 * Raw scan-code input; useful for games that read DirectInput scan codes.
 * scan is the 8-bit set-1 scan code; extended != 0 sets KEYEVENTF_EXTENDEDKEY.
 */
GP_API int  gp_input_key_scan(int scan, int extended, int down);

/* UTF-16 / UTF-8 text via KEYEVENTF_UNICODE. "\n" is sent as VK_RETURN. */
GP_API int  gp_input_text(const wchar_t* text);
GP_API int  gp_input_text_utf8(const char* text);

/* Vertical wheel; positive scrolls up. delta is clamped to +/-12000. */
GP_API int  gp_input_wheel(int delta);

/* Win32 GetLastError() value from the most recent failed call on this thread. */
GP_API int  gp_input_last_error(void);

#ifdef __cplusplus
}
#endif

#endif /* GOPLAY_NATIVE_H */
