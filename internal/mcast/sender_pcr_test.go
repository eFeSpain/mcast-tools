package mcast

import (
	"testing"
	"time"
)

// tsWithPCR construye un transport stream sintético con PCR cada `cada`
// paquetes, correspondiente a un flujo del bitrate indicado. Es lo que permite
// comprobar el pacing sin depender de tener material real a mano.
func tsWithPCR(packets, cada int, bitrate float64) []byte {
	ts := make([]byte, packets*tsPacket)
	for i := 0; i < packets; i++ {
		p := ts[i*tsPacket:]
		p[0] = 0x47
		p[1], p[2] = 0x01, 0x00 // PID 256
		if i%cada != 0 {
			p[3] = 0x10 // solo payload
			continue
		}
		p[3] = 0x30 // campo de adaptación + payload
		p[4] = 7    // longitud del campo
		p[5] = 0x10 // PCR_flag
		// El PCR que corresponde a este byte si el flujo va a `bitrate`.
		segundos := float64(i*tsPacket) * 8 / bitrate
		v := uint64(segundos * pcrClockHz)
		base, ext := v/300, v%300
		p[6] = byte(base >> 25)
		p[7] = byte(base >> 17)
		p[8] = byte(base >> 9)
		p[9] = byte(base >> 1)
		p[10] = byte(base<<7) | 0x7E | byte(ext>>8)
		p[11] = byte(ext)
	}
	return ts
}

func TestPCROf(t *testing.T) {
	ts := tsWithPCR(4, 2, 8_000_000)

	// El paquete 0 lleva PCR y vale 0; el 1 no lleva.
	if v, ok := pcrOf(ts[0:tsPacket]); !ok || v != 0 {
		t.Errorf("paquete 0: pcr = %d, ok = %v; quiero 0, true", v, ok)
	}
	if _, ok := pcrOf(ts[tsPacket : 2*tsPacket]); ok {
		t.Error("el paquete 1 no lleva PCR y se ha leído uno")
	}

	// El paquete 2 lleva el PCR correspondiente a 2 × 188 bytes a 8 Mbps.
	quiero := uint64(float64(2*tsPacket) * 8 / 8_000_000 * pcrClockHz)
	v, ok := pcrOf(ts[2*tsPacket : 3*tsPacket])
	if !ok {
		t.Fatal("el paquete 2 debería llevar PCR")
	}
	if d := int64(v) - int64(quiero); d > 300 || d < -300 {
		t.Errorf("pcr = %d, quiero ~%d", v, quiero)
	}

	// Basura y paquetes cortos no pueden colarse.
	if _, ok := pcrOf(make([]byte, tsPacket)); ok {
		t.Error("un paquete de ceros no lleva PCR")
	}
	if _, ok := pcrOf([]byte{0x47, 0, 0}); ok {
		t.Error("un paquete corto no puede dar PCR")
	}
}

// El pacer tiene que deducir el bitrate real del flujo a partir de sus PCR,
// que es justo lo que evita tener que acertar el -b a mano.
func TestPCRPacerLearnsTheRealBitrate(t *testing.T) {
	const real = 6_000_000 // 6 Mbps de verdad...
	ts := tsWithPCR(400, 10, real)
	// ...arrancando con una estimación deliberadamente mala: 20 Mbps.
	p := newPCRPacer(20_000_000 / 8)

	inicio := time.Now()
	chunk := 7 * tsPacket // 1316, el tamaño clásico
	var bytes float64
	for off := 0; off+chunk <= len(ts); off += chunk {
		p.observe(ts[off:off+chunk], bytes, inicio)
		bytes += float64(chunk)
	}

	if !p.started {
		t.Fatal("no ha encontrado ningún PCR")
	}
	medido := p.rate * 8 // bits/s
	if medido < real*0.95 || medido > real*1.05 {
		t.Fatalf("ritmo medido %.2f Mbps, el flujo es de %.2f Mbps",
			medido/1e6, float64(real)/1e6)
	}

	// Y el instante de salida de un byte tiene que corresponder a su posición
	// en el flujo: 1 MB a 6 Mbps son 1,4 s.
	bps := float64(real) // en una variable: si no, es una constante y no cabe en Duration
	quiero := time.Duration(float64(1<<20) * 8 / bps * float64(time.Second))
	got := p.target(1 << 20).Sub(inicio)
	if d := got - quiero; d > 100*time.Millisecond || d < -100*time.Millisecond {
		t.Fatalf("target(1 MB) = %v, quiero ~%v", got, quiero)
	}
}

// Un salto del reloj no puede provocar una ráfaga: ni la vuelta del contador de
// 33 bits ni un corte de material.
func TestPCRPacerSurvivesAClockJump(t *testing.T) {
	p := newPCRPacer(1_000_000)
	inicio := time.Now()

	normal := tsWithPCR(20, 10, 8_000_000)
	p.observe(normal, 0, inicio)
	ritmoAntes := p.rate

	// Un PCR que retrocede: contador que ha dado la vuelta.
	atras := make([]byte, tsPacket)
	copy(atras, normal[:tsPacket]) // PCR = 0, muy por detrás del último
	p.observe(atras, 1e6, inicio.Add(time.Second))

	if p.rate <= 0 || p.rate > 125_000_000 {
		t.Fatalf("el salto del reloj ha dejado un ritmo absurdo: %.0f B/s", p.rate)
	}
	if p.rate != ritmoAntes {
		t.Logf("ritmo reajustado de %.0f a %.0f B/s tras el salto", ritmoAntes, p.rate)
	}
	// Y el objetivo siguiente no puede quedar en el pasado remoto ni en el
	// futuro remoto: eso sería la ráfaga o el parón.
	d := p.target(1e6 + 1316).Sub(inicio.Add(time.Second))
	if d < -time.Second || d > time.Minute {
		t.Fatalf("tras el salto, el próximo objetivo cae a %v del ancla", d)
	}
}

func TestRTPHeader(t *testing.T) {
	w := newRTPWriter()
	w.seq, w.ssrc = 1000, 0xDEADBEEF
	pkt := make([]byte, rtpHeaderLen+10)

	w.header(pkt, w.start.Add(time.Second))

	if pkt[0] != 0x80 {
		t.Errorf("byte 0 = %#x, quiero 0x80 (versión 2, sin padding ni extensión)", pkt[0])
	}
	if pkt[1] != rtpPayloadTypeMP2T {
		t.Errorf("payload type = %d, quiero %d (MP2T)", pkt[1], rtpPayloadTypeMP2T)
	}
	if got := uint16(pkt[2])<<8 | uint16(pkt[3]); got != 1000 {
		t.Errorf("secuencia = %d, quiero 1000", got)
	}
	// Un segundo a 90 kHz son 90000 tics.
	ts := uint32(pkt[4])<<24 | uint32(pkt[5])<<16 | uint32(pkt[6])<<8 | uint32(pkt[7])
	if ts < 89000 || ts > 91000 {
		t.Errorf("marca de tiempo = %d, quiero ~90000 (1 s a 90 kHz)", ts)
	}
	if got := uint32(pkt[8])<<24 | uint32(pkt[9])<<16 | uint32(pkt[10])<<8 | uint32(pkt[11]); got != 0xDEADBEEF {
		t.Errorf("SSRC = %#x, quiero 0xDEADBEEF", got)
	}
	if w.seq != 1001 {
		t.Errorf("la secuencia no ha avanzado: %d", w.seq)
	}

	// Y tiene que envolver, no desbordar.
	w.seq = 65535
	w.header(pkt, w.start)
	if w.seq != 0 {
		t.Errorf("la secuencia no envuelve: %d", w.seq)
	}
}

// Dos emisores distintos no pueden arrancar con la misma secuencia ni el mismo
// SSRC: un receptor que ve reaparecer un SSRC conocido cree que es el mismo
// flujo de antes.
func TestRTPWritersStartDifferent(t *testing.T) {
	a, b := newRTPWriter(), newRTPWriter()
	if a.ssrc == b.ssrc && a.seq == b.seq {
		t.Fatal("dos emisores arrancan idénticos: el SSRC y la secuencia deben ser aleatorios")
	}
}
