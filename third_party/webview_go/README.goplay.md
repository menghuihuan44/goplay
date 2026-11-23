# Vendored webview_go (GoPlay patch)

Upstream: https://github.com/webview/webview_go
Version: v0.0.0-20240831120633-6173450d4dd6 (latest as of 2026-11)
License: MIT (see LICENSE)

GoPlay change in libs/webview/include/webview.h: after WebView2 creates its
controller we set COREWEBVIEW2_BOUNDS_MODE_USE_RAW_PIXELS. The default DIP
bounds caused the embedded dashboard to render at 2x on 200% Windows displays
while the host HWND stayed at physical size, clipping the right half of the UI.
The desktop shell also re-applies its logical window size once the window is on
a monitor so webview_go scales it for the actual DPI.

When updating this patch, bump goplay_patch.go's revision so Go's cgo build
cache does not reuse the previous header compilation.
