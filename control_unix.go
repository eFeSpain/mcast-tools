//go:build !windows && !linux

package main

import "golang.org/x/sys/unix"

// setReuse permite que varios relés bindeen el MISMO puerto a la vez. En los
// BSD (y macOS) un bind duplicado exige SO_REUSEPORT además de SO_REUSEADDR.
//
// Aquí no hace falta el equivalente a IP_MULTICAST_ALL de Linux: los BSD
// entregan a cada socket solo los grupos que ese socket unió. Es justo la
// diferencia que motivó esa opción en Linux, cuyo valor por defecto (1) hace lo
// contrario. Sin comprobar en esta plataforma por falta de una máquina BSD.
func setReuse(fd uintptr) {
	unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
	unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1)
}
