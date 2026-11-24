// Package domain holds the shared, dependency-free value types.
package domain

import "errors"

// ErrTransient marks a failure that is expected to heal on its own: a missing
// Android client, a dropped WebSocket, or a frame gap while the phone rotates
// or reconnects. The engine must never turn these into a terminal instance
// error; it shows "等待画面" and retries on the next tick.
var ErrTransient = errors.New("transient bridge condition")
