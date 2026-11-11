//go:build !linux

package machine

// socketsReceived: only Linux says what a process's sockets received.
func socketsReceived(pids []int) uint64 { return 0 }
