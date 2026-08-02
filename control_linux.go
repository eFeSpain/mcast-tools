//go:build linux

package main

import "golang.org/x/sys/unix"

// setReuse prepara el socket de recepción:
//
//   - SO_REUSEADDR permite que varios relés bindeen el MISMO puerto a la vez
//     (p.ej. dos canales que leen el mismo origen y lo mandan a sitios
//     distintos). Para UDP basta con esto; SO_REUSEPORT no se usa a propósito,
//     porque su semántica es repartir la carga entre los sockets del grupo, no
//     entregar una copia a cada uno.
//
//   - IP_MULTICAST_ALL=0 es imprescindible: por defecto Linux entrega a un
//     socket bindeado al comodín TODOS los grupos unidos en el host que lleguen
//     a su puerto, no solo los que ese socket unió. Sin esto, dos canales con
//     el mismo puerto de origen y grupos distintos se reenvían mutuamente el
//     flujo del vecino. Y bindear al grupo en vez de al comodín no vale como
//     alternativa: el paquete net reescribe cualquier bind multicast a 0.0.0.0
//     (ver net/sock_posix.go, listenDatagram).
func setReuse(fd uintptr) {
	unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
	unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MULTICAST_ALL, 0)
}
