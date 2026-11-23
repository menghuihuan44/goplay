//go:build windows

package localmodel

import "os"

// requestStop on Windows uses Kill: a GUI process cannot reliably deliver
// SIGTERM/CTRL_BREAK to a child without a shared console or job object, and
// goplay does not install a kernel driver or hook the game process. Stop still
// waits for exit and is idempotent.
func requestStop(p *os.Process) error {
	return p.Kill()
}
