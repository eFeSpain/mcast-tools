package mcast

import (
	"crypto/rand"
	"encoding/binary"
	"time"
)

// ─── Encapsulado RTP (RFC 3550) ──────────────────────────────────────────────

// rtpHeaderLen son los 12 bytes de la cabecera fija. Por eso el payload por
// defecto son 1316 (7 × 188): 1316 + 12 = 1328, que cabe holgado en una MTU de
// 1500 sin fragmentar.
const rtpHeaderLen = 12

// rtpPayloadTypeMP2T es el 33 que RFC 3551 reserva para MPEG-2 Transport
// Stream, y el reloj de sus marcas de tiempo son 90 kHz.
const (
	rtpPayloadTypeMP2T = 33
	rtpClockHz         = 90000
)

// rtpWriter pone la cabecera delante de cada datagrama. Hace falta para SMPTE
// 2022-2 y para los decodificadores profesionales que la esperan; de paso, el
// número de secuencia deja que el receptor detecte pérdidas y reordenaciones,
// cosa que con UDP a pelo es imposible.
type rtpWriter struct {
	seq  uint16
	ssrc uint32
	// start ancla el reloj de 90 kHz al instante de arranque.
	start time.Time
}

func newRTPWriter() *rtpWriter {
	// Secuencia y SSRC iniciales aleatorios, como manda RFC 3550: dos flujos
	// distintos no deben empezar iguales, y un receptor que ve reaparecer un
	// SSRC conocido creería que es el mismo flujo de antes.
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Sin aleatoriedad se sigue emitiendo: es preferible a no emitir.
		b = [6]byte{}
	}
	return &rtpWriter{
		seq:   binary.BigEndian.Uint16(b[0:2]),
		ssrc:  binary.BigEndian.Uint32(b[2:6]),
		start: time.Now(),
	}
}

// header escribe la cabecera en los 12 primeros bytes de pkt. La marca de
// tiempo se saca del reloj real y no del contador de paquetes: si el emisor se
// retrasa, la marca lo refleja en vez de mentir.
func (w *rtpWriter) header(pkt []byte, now time.Time) {
	pkt[0] = 0x80 // versión 2, sin padding, sin extensión, sin CSRC
	pkt[1] = rtpPayloadTypeMP2T
	binary.BigEndian.PutUint16(pkt[2:4], w.seq)
	ts := uint32(now.Sub(w.start).Seconds() * rtpClockHz)
	binary.BigEndian.PutUint32(pkt[4:8], ts)
	binary.BigEndian.PutUint32(pkt[8:12], w.ssrc)
	w.seq++ // envuelve solo, que es justo lo que espera el receptor
}
