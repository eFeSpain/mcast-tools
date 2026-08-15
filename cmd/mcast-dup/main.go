// mcast-dup: duplica flujos multicast UDP de un grupo a otro(s).
//
//	mcast-dup -s ORIGEN:PUERTO -d DEST1,DEST2 [opciones]
//	mcast-dup -config /etc/mcast-dup.json      (recargar: kill -HUP <pid>)
//
// Toda la lógica vive en internal/mcast, que se comparte con mcast-send.
package main

import "mcast-tools/internal/mcast"

func main() { mcast.RelayMain() }
