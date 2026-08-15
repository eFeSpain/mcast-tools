package mcast

import "time"

// ─── Pacing guiado por el PCR del transport stream ───────────────────────────
//
// El pacing a bitrate constante obliga a acertar el número: si le pides 10 Mbps
// a un TS que en realidad son 6, lo emites 1,6 veces más rápido y le revientas
// el búfer al decodificador; si te quedas corto, lo matas de hambre. Y con
// material de bitrate variable no hay número correcto.
//
// El propio transport stream lleva la respuesta. El PCR (Program Clock
// Reference) es el reloj del multiplexor, en unidades de 27 MHz, y viaja en el
// campo de adaptación de algunos paquetes. Emitir "al ritmo del PCR" es
// reproducir el flujo exactamente al ritmo al que se creó.

// pcrClockHz son las unidades del PCR: 27 MHz.
const pcrClockHz = 27_000_000

// pcrProbeBytes es cuánto se busca un PCR antes de rendirse y volver al bitrate
// configurado. Un TS bien formado trae uno cada 40 ms (DVB) o 100 ms (norma),
// así que con 4 MB hay de sobra incluso a 100 Mbps.
const pcrProbeBytes = 4 << 20

// pcrOf extrae el PCR de un paquete TS de 188 bytes, en unidades de 27 MHz.
//
// Estructura: byte 3 bit 0x20 dice que hay campo de adaptación; byte 4 es su
// longitud; byte 5 lleva las banderas y 0x10 es PCR_flag; y entonces los bytes
// 6..11 son 33 bits de base a 90 kHz, 6 de relleno y 9 de extensión a 27 MHz.
func pcrOf(p []byte) (uint64, bool) {
	if len(p) < 12 || p[0] != 0x47 {
		return 0, false
	}
	if p[3]&0x20 == 0 { // sin campo de adaptación
		return 0, false
	}
	if p[4] < 7 { // no cabe un PCR
		return 0, false
	}
	if p[5]&0x10 == 0 { // sin PCR
		return 0, false
	}
	base := uint64(p[6])<<25 | uint64(p[7])<<17 | uint64(p[8])<<9 |
		uint64(p[9])<<1 | uint64(p[10])>>7
	ext := uint64(p[10]&0x01)<<8 | uint64(p[11])
	return base*300 + ext, true
}

// pcrPacer traduce posiciones en bytes a instantes de emisión siguiendo el
// reloj del propio flujo.
//
// El esquema es un ancla absoluta más interpolación: en cada PCR se sabe el
// instante exacto en que ese byte debe salir, y entre dos PCR se reparte
// proporcionalmente con el ritmo medido entre ellos. Al anclarse al reloj del
// flujo y no a un acumulado, no hay deriva por mucho que dure la emisión.
type pcrPacer struct {
	rate       float64   // bytes/s medidos entre los dos últimos PCR
	anchorWall time.Time // instante real del primer PCR
	anchorPCR  uint64    // valor de ese primer PCR
	lastPCR    uint64    // último PCR visto
	lastBytes  float64   // posición en bytes de ese último PCR
	started    bool
	// gaveUp indica que no se ha encontrado ningún PCR y se ha vuelto al
	// bitrate configurado.
	gaveUp bool
}

func newPCRPacer(bootstrapRate float64) *pcrPacer {
	return &pcrPacer{rate: bootstrapRate}
}

// observe mira un trozo de flujo en busca de PCR. bytesBefore es la posición
// del primer byte del trozo dentro del total emitido. Devuelve true si el trozo
// traía al menos un PCR.
func (p *pcrPacer) observe(chunk []byte, bytesBefore float64, now time.Time) bool {
	visto := false
	for off := 0; off+tsPacket <= len(chunk); off += tsPacket {
		v, ok := pcrOf(chunk[off : off+tsPacket])
		if !ok {
			continue
		}
		pos := bytesBefore + float64(off)
		if !p.started {
			p.started, p.anchorWall, p.anchorPCR = true, now, v
			p.lastPCR, p.lastBytes = v, pos
			visto = true
			continue
		}
		// Entre dos PCR se puede medir el ritmo real del flujo.
		dPCR := int64(v) - int64(p.lastPCR)
		dBytes := pos - p.lastBytes
		if dPCR > 0 && dBytes > 0 {
			seg := float64(dPCR) / pcrClockHz
			if r := dBytes / seg; r > 8_000 && r < 125_000_000 {
				// Entre 64 kbps y 1 Gbps: fuera de ahí es una discontinuidad
				// del reloj o un salto de material, y el ritmo medido sería
				// una barbaridad que dispararía una ráfaga.
				p.rate = r
			}
		}
		if dPCR <= 0 || dPCR > 10*pcrClockHz {
			// El PCR ha dado un salto atrás (vuelta del contador de 33 bits,
			// cada ~26 h) o un salto adelante enorme (corte de material). Se
			// vuelve a anclar aquí en vez de intentar recuperar el desfase.
			p.anchorWall, p.anchorPCR = now, v
		}
		p.lastPCR, p.lastBytes = v, pos
		visto = true
	}
	return visto
}

// target devuelve cuándo debe haber salido la posición de bytes indicada.
func (p *pcrPacer) target(bytes float64) time.Time {
	if !p.started {
		return time.Time{} // aún sin referencia: manda el bitrate configurado
	}
	desdeAncla := float64(p.lastPCR-p.anchorPCR) / pcrClockHz
	interpolado := (bytes - p.lastBytes) / p.rate
	return p.anchorWall.Add(time.Duration((desdeAncla + interpolado) * float64(time.Second)))
}
