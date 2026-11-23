module goplay

go 1.27.4

require (
	github.com/gorilla/websocket v1.5.3
	github.com/webview/webview_go v0.0.0-20240831120633-6173450d4dd6
	github.com/yuin/gopher-lua v1.1.1
)

// Local WebView2 host patched for raw-pixel bounds on high-DPI displays.
replace github.com/webview/webview_go => ./third_party/webview_go
