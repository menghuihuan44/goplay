//go:build !windows

package localmodel

import (
	"os"
	"syscall"
)

// requestStop sends SIGTERM on Unix-like systems so llama-server can release
// its listening socket and exit cleanly. Stop escalates to Kill on timeout.
func requestStop(p *os.Process) error {
	return p.Signal(syscall.SIGTERM)
}
