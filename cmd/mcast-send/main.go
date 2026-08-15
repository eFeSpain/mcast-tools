// mcast-send: emite un flujo multicast UDP a un ritmo controlado.
//
//	mcast-send -d GRUPO:PUERTO -f fichero.ts -b 10M
//	ffmpeg ... -f mpegts - | mcast-send -d GRUPO:PUERTO -stdin -b 8M
//	mcast-send -config /etc/mcast-send.json    (recargar: kill -HUP <pid>)
//
// Comparte con mcast-dup todo internal/mcast: configuración, interfaz, sockets,
// orquestación de canales, estadísticas y mensajes.
package main

import "mcast-tools/internal/mcast"

func main() { mcast.SendMain() }
