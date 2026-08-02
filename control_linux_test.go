//go:build linux

package main

import (
	"context"
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
