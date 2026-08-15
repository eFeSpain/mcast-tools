package mcast

import (
	"testing"
	"time"
)

// tsPacketWithPCR construye un paquete TS de 188 bytes con el PCR indicado, en
// unidades de 27 MHz.
func tsPacketWithPCR(v uint64) []byte {
	p := make([]byte, tsPacket)
	p[0] = 0x47
	p[1], p[2] = 0x01, 0x00 // PID 256
	p[3] = 0x30             // campo de adaptación + payload
	p[4] = 7                // longitud del campo
	p[5] = 0x10             // PCR_flag
	base, ext := v/300, v%300
	p[6] = byte(base >> 25)
	p[7] = byte(base >> 17)
	p[8] = byte(base >> 9)
	p[9] = byte(base >> 1)
	p[10] = byte(base<<7) | 0x7E | byte(ext>>8)
	p[11] = byte(ext)
	return p
}

// pcrAt devuelve el PCR que corresponde a la posición `offset` de un flujo que
// va al bitrate indicado.
func pcrAt(offset int, bitrate float64) uint64 {
	return uint64(float64(offset) * 8 / bitrate * pcrClockHz)
}

// tsWithPCR construye un transport stream sintético con PCR cada `cada`
// paquetes, correspondiente a un flujo del bitrate indicado. Es lo que permite
// comprobar el pacing sin depender de tener material real a mano.
func tsWithPCR(packets, cada int, bitrate float64) []byte {
	ts := make([]byte, packets*tsPacket)
	for i := 0; i < packets; i++ {
		p := ts[i*tsPacket : (i+1)*tsPacket]
		if i%cada != 0 {
			p[0] = 0x47
			p[1], p[2] = 0x01, 0x00
			p[3] = 0x10 // solo payload
			continue
		}
		copy(p, tsPacketWithPCR(pcrAt(i*tsPacket, bitrate)))
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

// Un salto del reloj no puede dejar el canal mudo ni provocar una ráfaga. Se
// prueban las tres formas en que ocurre: el contador de 33 bits que da la
// vuelta, el material empalmado que arranca con un PCR muy por delante, y la
// discontinuidad que el propio flujo declara.
//
// El margen es estrecho a propósito: con uno ancho, este test pasaba aunque se
// borrara entera la lógica de re-anclaje que le da nombre.
func TestPCRPacerReanchorsOnAClockJump(t *testing.T) {
	const bitrate = 8_000_000
	// El PCR que le tocaría al paquete siguiente si el flujo continuara.
	siguiente := pcrAt(200*tsPacket, bitrate)
	casos := []struct {
		nombre string
		pcr    uint64
	}{
		{"el contador de 33 bits da la vuelta", 0},
		{"material empalmado 30 s por delante", siguiente + 30*pcrClockHz},
	}

	for _, c := range casos {
		// 10 segundos de reloj de pared desde el ancla, para que sin re-anclaje
		// el error sea de segundos y el test no pueda pasar por casualidad.
		p := newPCRPacer(bitrate / 8)
		inicio := time.Now()
		normal := tsWithPCR(200, 10, bitrate)
		p.observe(normal, 0, inicio)
		bytesYa := float64(len(normal))

		ahora := inicio.Add(10 * time.Second)
		p.observe(tsPacketWithPCR(c.pcr), bytesYa, ahora)

		// Tras re-anclar, el instante del byte siguiente tiene que caer
		// pegado a "ahora": un datagrama de 1316 B a 8 Mbps son 1,3 ms.
		d := p.target(bytesYa + 1316).Sub(ahora)
		if d < 0 || d > 50*time.Millisecond {
			t.Errorf("%s: el objetivo cae a %v del salto; sin re-anclar sería de segundos", c.nombre, d)
		}
		if p.rate <= 0 || p.rate > 125_000_000 {
			t.Errorf("%s: ritmo absurdo tras el salto: %.0f B/s", c.nombre, p.rate)
		}
	}
}

// La discontinuidad declarada por el flujo (discontinuity_indicator) hay que
// respetarla aunque el salto de PCR sea pequeño: es la señal explícita.
func TestPCRPacerHonoursTheDiscontinuityFlag(t *testing.T) {
	p := newPCRPacer(1_000_000)
	inicio := time.Now()
	normal := tsWithPCR(200, 10, 8_000_000)
	p.observe(normal, 0, inicio)
	bytesYa := float64(len(normal))

	// Un paquete con el PCR que le tocaba —un salto tan pequeño que la
	// heurística de maxPCRJump no lo ve— pero con la discontinuidad declarada.
	roto := tsPacketWithPCR(pcrAt(200*tsPacket, 8_000_000))
	roto[5] |= 0x80 // discontinuity_indicator
	ahora := inicio.Add(5 * time.Second)
	p.observe(roto, bytesYa, ahora)

	if d := p.target(bytesYa + 1316).Sub(ahora); d < 0 || d > 50*time.Millisecond {
		t.Fatalf("no se ha re-anclado con la discontinuidad declarada: objetivo a %v", d)
	}
}

// Un paquete marcado como corrupto no puede anclar el reloj de toda la emisión.
func TestPCROfIgnoresCorruptPackets(t *testing.T) {
	ts := tsWithPCR(1, 1, 8_000_000)
	if _, ok := pcrOf(ts); !ok {
		t.Fatal("el paquete de referencia debería llevar PCR")
	}
	ts[1] |= 0x80 // transport_error_indicator
	if _, ok := pcrOf(ts); ok {
		t.Fatal("se ha leído el PCR de un paquete marcado como corrupto")
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
	// Con && bastaba con que uno de los dos fuera aleatorio: un SSRC constante
	// pasaba el test sin que nadie se enterase. Cada campo, por separado.
	a, b := newRTPWriter(), newRTPWriter()
	if a.ssrc == b.ssrc {
		t.Errorf("dos emisores arrancan con el mismo SSRC (%#x): un receptor creerá que es el mismo flujo", a.ssrc)
	}
	if a.seq == b.seq {
		t.Errorf("dos emisores arrancan con la misma secuencia (%d)", a.seq)
	}
	if a.ssrc == 0 {
		t.Error("SSRC 0: no se ha inicializado")
	}
}
