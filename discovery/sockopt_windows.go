//go:build windows

package discovery

import (
	"net"
	"syscall"
)

func enableBroadcast(conn *net.UDPConn) error {
	rawConn, err := conn.SyscallConn()
	if err != nil {
		return err
	}
	return rawConn.Control(func(fd uintptr) {
		// SO_BROADCAST = 0x20 on Windows
		syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, 0x20, 1)
	})
}
