//go:build windows

package mcast

import "syscall"

// setReuse permite que varios relés compartan el puerto (no existe
// SO_REUSEPORT en Windows).
//
// Aquí tampoco hace falta el equivalente a IP_MULTICAST_ALL de Linux:
// comprobado que Windows entrega a cada socket solo los grupos que ese socket
// unió, aunque todos estén bindeados al comodín y compartan puerto.
func setReuse(fd uintptr) {
	syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
