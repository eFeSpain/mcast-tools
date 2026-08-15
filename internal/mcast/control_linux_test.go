//go:build linux

package mcast

import (
	"context"
	"fmt"
	"net"
	"testing"

	"golang.org/x/sys/unix"
)

// Sin IP_MULTICAST_ALL=0 el socket RX recibe TODOS los grupos unidos en el
// host que lleguen a su puerto, no solo el suyo: dos canales que comparten
// puerto se reenvían el flujo del vecino.
func TestRxSocketDisablesMulticastAll(t *testing.T) {
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
		got, gerr = unix.GetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MULTICAST_ALL)
	}); err != nil {
		t.Fatalf("Control: %v", err)
	}
	if gerr != nil {
		t.Fatalf("getsockopt IP_MULTICAST_ALL: %v", gerr)
	}
	if got != 0 {
		t.Fatalf("IP_MULTICAST_ALL = %d, quiero 0", got)
	}
}

// Sin SO_REUSEADDR, dos canales no pueden compartir el mismo puerto de origen:
// es el motivo de ser de setReuse y no estaba cubierto.
func TestRxSocketSetsReuseAddr(t *testing.T) {
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
		got, gerr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR)
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

// Dos sockets en el mismo puerto tienen que poder convivir de verdad, no solo
// tener la opción puesta.
func TestTwoRxSocketsCanShareThePort(t *testing.T) {
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
