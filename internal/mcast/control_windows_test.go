//go:build windows

package mcast

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"testing"
)

// En Windows, SO_REUSEADDR es lo único que permite que dos canales compartan el
// puerto de origen. No estaba cubierto en esta plataforma.
func TestRxSocketSetsReuseAddrOnWindows(t *testing.T) {
	lc := net.ListenConfig{Control: reuseControl}
	pc, err := lc.ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	defer pc.Close()

	raw, err := pc.(*net.UDPConn).SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var got int
	var gerr error
	if err := raw.Control(func(fd uintptr) {
		got, gerr = syscall.GetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR)
	}); err != nil {
		t.Fatalf("Control: %v", err)
	}
	if gerr != nil {
		t.Fatalf("getsockopt SO_REUSEADDR: %v", gerr)
	}
	if got == 0 {
		t.Fatal("SO_REUSEADDR sin activar: dos canales no podrían compartir puerto de origen")
	}
}

func TestTwoRxSocketsCanSharethePortOnWindows(t *testing.T) {
	lc := net.ListenConfig{Control: reuseControl}
	first, err := lc.ListenPacket(context.Background(), "udp4", ":0")
	if err != nil {
		t.Fatalf("primer bind: %v", err)
	}
	defer first.Close()

	port := first.LocalAddr().(*net.UDPAddr).Port
	second, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatalf("segundo bind al puerto %d: %v", port, err)
	}
	second.Close()
}
