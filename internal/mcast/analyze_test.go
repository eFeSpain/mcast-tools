package mcast

import (
	"strings"
	"testing"
)

// paq construye un paquete TS a medida. Los tests del analizador necesitan
// fabricar situaciones concretas —un contador que salta, una discontinuidad
// declarada, un PCR sin extensión— que no se pueden sacar de material real.
type paq struct {
	pid      uint16
	cc       uint8
	payload  bool
	af       bool
	discont  bool
	tei      bool
	pusi     bool
	scram    bool
	pcr      uint64
	tienePCR bool
	datos    []byte // se copia detrás de la cabecera y del campo de adaptación
}

func mkTS(o paq) []byte {
	p := make([]byte, tsPacket)
	p[0] = 0x47
	p[1] = byte(o.pid >> 8)
	if o.tei {
		p[1] |= 0x80
	}
	if o.pusi {
		p[1] |= 0x40
	}
	p[2] = byte(o.pid)

	afc := byte(0)
	if o.af || o.discont || o.tienePCR {
		afc |= 0x02
	}
	if o.payload {
		afc |= 0x01
	}
	p[3] = afc<<4 | (o.cc & 0x0F)
	if o.scram {
		p[3] |= 0x80
	}

	off := 4
	if afc&0x02 != 0 {
		flags := byte(0)
		if o.discont {
			flags |= 0x80
		}
		largo := 1 // solo el byte de banderas
		if o.tienePCR {
			flags |= 0x10
			largo = 7
		}
		p[4] = byte(largo)
		p[5] = flags
		if o.tienePCR {
			base, ext := o.pcr/300, o.pcr%300
			p[6] = byte(base >> 25)
			p[7] = byte(base >> 17)
			p[8] = byte(base >> 9)
			p[9] = byte(base >> 1)
			p[10] = byte(base<<7) | 0x7E | byte(ext>>8)
			p[11] = byte(ext)
		}
		off = 5 + largo
	}
	copy(p[off:], o.datos)
	return p
}

// ─── Lo que entra ────────────────────────────────────────────────────────────

func TestPayloadOfRecognisesRawAndRTP(t *testing.T) {
	crudo := mkTS(paq{pid: 100, payload: true})
	if got := payloadOf(crudo); len(got) != tsPacket {
		t.Errorf("TS a pelo: devuelve %d bytes, quiero %d", len(got), tsPacket)
	}

	// Encapsulado en RTP: 12 bytes de cabecera por delante. Si no se
	// reconociera, todo flujo RTP se contaría como basura y el analizador
	// sería ruido en vez de información.
	rtp := make([]byte, rtpHeaderLen+2*tsPacket)
	rtp[0], rtp[1] = 0x80, rtpPayloadTypeMP2T
	copy(rtp[rtpHeaderLen:], crudo)
	copy(rtp[rtpHeaderLen+tsPacket:], crudo)
	got := payloadOf(rtp)
	if len(got) != 2*tsPacket {
		t.Fatalf("TS sobre RTP: devuelve %d bytes, quiero %d", len(got), 2*tsPacket)
	}
	if got[0] != 0x47 {
		t.Errorf("la cabecera RTP no se ha saltado: primer byte %#x", got[0])
	}

	if payloadOf([]byte("esto no es un transport stream en absoluto")) != nil {
		t.Error("se ha aceptado como TS algo que no lo es")
	}
}

// ─── Continuidad: la métrica que más vale y la más fácil de hacer mal ────────

func TestContinuityAcceptsWhatTheStandardAllows(t *testing.T) {
	casos := []struct {
		nombre string
		paqs   []paq
		quiero uint64
	}{
		{"flujo limpio", []paq{
			{pid: 100, cc: 0, payload: true}, {pid: 100, cc: 1, payload: true},
			{pid: 100, cc: 2, payload: true}, {pid: 100, cc: 3, payload: true},
		}, 0},
		{"el contador envuelve de 15 a 0", []paq{
			{pid: 100, cc: 14, payload: true}, {pid: 100, cc: 15, payload: true},
			{pid: 100, cc: 0, payload: true},
		}, 0},
		{"un duplicado exacto está permitido", []paq{
			{pid: 100, cc: 5, payload: true}, {pid: 100, cc: 5, payload: true},
			{pid: 100, cc: 6, payload: true},
		}, 0},
		{"sin payload el contador NO avanza", []paq{
			{pid: 100, cc: 7, payload: true},
			{pid: 100, cc: 7, af: true}, // solo campo de adaptación
			{pid: 100, cc: 8, payload: true},
		}, 0},
		{"discontinuidad declarada no es error", []paq{
			{pid: 100, cc: 3, payload: true},
			{pid: 100, cc: 9, payload: true, discont: true},
			{pid: 100, cc: 10, payload: true},
		}, 0},
		{"cada PID lleva su propio contador", []paq{
			{pid: 100, cc: 0, payload: true}, {pid: 200, cc: 7, payload: true},
			{pid: 100, cc: 1, payload: true}, {pid: 200, cc: 8, payload: true},
		}, 0},

		{"un paquete perdido SÍ es error", []paq{
			{pid: 100, cc: 0, payload: true}, {pid: 100, cc: 2, payload: true},
		}, 1},
		{"dos duplicados seguidos SÍ son error", []paq{
			{pid: 100, cc: 5, payload: true}, {pid: 100, cc: 5, payload: true},
			{pid: 100, cc: 5, payload: true},
		}, 1},
		{"el contador retrocede", []paq{
			{pid: 100, cc: 8, payload: true}, {pid: 100, cc: 3, payload: true},
		}, 1},
	}

	for _, c := range casos {
		a := newTSAnalyzer()
		for _, o := range c.paqs {
			a.feed(mkTS(o))
		}
		if a.ccErrs != c.quiero {
			t.Errorf("%s: %d errores de continuidad, quiero %d", c.nombre, a.ccErrs, c.quiero)
		}
	}
}

// Los nulos no llevan contador definido: contarlos daría un torrente de falsos
// positivos, porque un flujo CBR va lleno de ellos.
func TestNullPacketsAreNotCountedAsContinuityErrors(t *testing.T) {
	a := newTSAnalyzer()
	for i := 0; i < 20; i++ {
		a.feed(mkTS(paq{pid: nullPID, cc: uint8(i * 7), payload: true}))
	}
	if a.ccErrs != 0 {
		t.Errorf("%d errores de continuidad por paquetes nulos", a.ccErrs)
	}
	if a.nulls != 20 {
		t.Errorf("nulos contados = %d, quiero 20", a.nulls)
	}
}

// El aviso tiene que nombrar el PID culpable: "hay errores" no sirve de nada a
// las tres de la mañana, "PID 101" sí.
func TestReportNamesTheWorstPID(t *testing.T) {
	a := newTSAnalyzer()
	a.feed(mkTS(paq{pid: 101, cc: 0, payload: true}))
	a.feed(mkTS(paq{pid: 202, cc: 0, payload: true}))
	// Un error en el 202 y tres en el 101.
	a.feed(mkTS(paq{pid: 202, cc: 5, payload: true}))
	for _, cc := range []uint8{5, 9, 14} {
		a.feed(mkTS(paq{pid: 101, cc: cc, payload: true}))
	}

	got := a.snapshot()
	if !strings.Contains(got, "101") {
		t.Errorf("el aviso no nombra el PID con más errores: %q", got)
	}
}

// ─── Reloj ───────────────────────────────────────────────────────────────────

func TestClockFindings(t *testing.T) {
	const pid = 100
	// Un PCR con extensión distinta de cero, para que el aviso de "sin
	// extensión" no salte donde no toca.
	pcr := func(ms float64) uint64 { return uint64(ms*pcrPerMs) + 1 }

	t.Run("flujo sano no genera avisos", func(t *testing.T) {
		a := newTSAnalyzer()
		for i := 0; i < 10; i++ {
			a.feed(mkTS(paq{pid: pid, payload: true, cc: uint8(i), tienePCR: true, pcr: pcr(float64(i) * 20)}))
		}
		if got := a.snapshot(); got != "" {
			t.Errorf("un flujo sano ha generado avisos: %q", got)
		}
	})

	t.Run("intervalo de PCR por encima del limite", func(t *testing.T) {
		a := newTSAnalyzer()
		for i := 0; i < 5; i++ {
			a.feed(mkTS(paq{pid: pid, payload: true, cc: uint8(i), tienePCR: true, pcr: pcr(float64(i) * 80)}))
		}
		got := a.snapshot()
		if !strings.Contains(got, "PCR") || !strings.Contains(got, "80") {
			t.Errorf("no se avisa del intervalo de 80 ms (tope %d): %q", maxPCRGap, got)
		}
	})

	t.Run("salto sin discontinuity_indicator", func(t *testing.T) {
		a := newTSAnalyzer()
		a.feed(mkTS(paq{pid: pid, payload: true, cc: 0, tienePCR: true, pcr: pcr(0)}))
		a.feed(mkTS(paq{pid: pid, payload: true, cc: 1, tienePCR: true, pcr: pcr(20)}))
		a.feed(mkTS(paq{pid: pid, payload: true, cc: 2, tienePCR: true, pcr: pcr(30000)})) // +30 s
		if got := a.snapshot(); !strings.Contains(got, "discontinuity_indicator") {
			t.Errorf("no se avisa del salto de reloj sin declarar: %q", got)
		}
	})

	t.Run("salto declarado no se avisa", func(t *testing.T) {
		a := newTSAnalyzer()
		a.feed(mkTS(paq{pid: pid, payload: true, cc: 0, tienePCR: true, pcr: pcr(0)}))
		a.feed(mkTS(paq{pid: pid, payload: true, cc: 1, tienePCR: true, pcr: pcr(20)}))
		a.feed(mkTS(paq{pid: pid, payload: true, cc: 2, tienePCR: true, pcr: pcr(30000), discont: true}))
		if got := a.snapshot(); strings.Contains(got, "salto") || strings.Contains(got, "jump") {
			t.Errorf("se avisa de un salto que el flujo declaró: %q", got)
		}
	})

	t.Run("sin extension de 27 MHz", func(t *testing.T) {
		// Este es el defecto que tienen todos los muxers TS en Go y el ffmpeg
		// sin -muxrate: la base a 90 kHz y la extensión a cero. El reloj queda
		// cuantizado a 11,1 µs y no hay forma de ser conforme.
		a := newTSAnalyzer()
		for i := 0; i < 5; i++ {
			v := uint64(float64(i)*20*pcrPerMs) / 300 * 300 // extensión exactamente 0
			a.feed(mkTS(paq{pid: pid, payload: true, cc: uint8(i), tienePCR: true, pcr: v}))
		}
		if got := a.snapshot(); !strings.Contains(got, "27 MHz") {
			t.Errorf("no se avisa de la extensión a cero: %q", got)
		}
	})

	t.Run("sin PCR ninguno", func(t *testing.T) {
		a := newTSAnalyzer()
		for i := 0; i < 5; i++ {
			a.feed(mkTS(paq{pid: pid, payload: true, cc: uint8(i)}))
		}
		got := a.snapshot()
		if !strings.Contains(got, "PCR") {
			t.Errorf("no se avisa de que el flujo no trae reloj: %q", got)
		}
	})
}

// ─── Lo que lleva dentro ─────────────────────────────────────────────────────

// pat y pmt fabrican las dos tablas mínimas que hacen falta para que el
// analizador pueda decir qué lleva el flujo.
func patSection(programa, pmtPID uint16) []byte {
	cuerpo := []byte{
		byte(programa >> 8), byte(programa),
		byte(pmtPID>>8) | 0xE0, byte(pmtPID),
	}
	return psi(0x00, 0x0001, cuerpo)
}

func pmtSection(pcrPID uint16, flujos map[uint16]uint8) []byte {
	cuerpo := []byte{byte(pcrPID>>8) | 0xE0, byte(pcrPID), 0xF0, 0x00}
	// Orden estable para que el test no dependa del recorrido del mapa.
	for _, pid := range []uint16{101, 102, 103} {
		if t, ok := flujos[pid]; ok {
			cuerpo = append(cuerpo, t, byte(pid>>8)|0xE0, byte(pid), 0xF0, 0x00)
		}
	}
	return psi(0x02, 0x0001, cuerpo)
}

// psi envuelve un cuerpo con la cabecera de sección y un CRC de relleno: el
// analizador no lo verifica, así que basta con que ocupe.
func psi(tableID byte, ext uint16, cuerpo []byte) []byte {
	n := 5 + len(cuerpo) + 4 // cabecera tras section_length + cuerpo + CRC
	s := []byte{tableID, byte(n>>8) | 0xB0, byte(n),
		byte(ext >> 8), byte(ext), 0xC1, 0x00, 0x00}
	s = append(s, cuerpo...)
	return append(s, 0, 0, 0, 0)
}

func conPointer(s []byte) []byte {
	return append([]byte{0x00}, s...) // pointer_field = 0
}

func TestReportsWhatTheStreamCarries(t *testing.T) {
	a := newTSAnalyzer()
	a.feed(mkTS(paq{pid: patPID, cc: 0, payload: true, pusi: true,
		datos: conPointer(patSection(1, 4096))}))
	a.feed(mkTS(paq{pid: 4096, cc: 0, payload: true, pusi: true,
		datos: conPointer(pmtSection(101, map[uint16]uint8{101: 0x1B, 102: 0x0F, 103: 0x81}))}))

	got := a.contents()
	for _, quiero := range []string{"101", "H.264", "102", "AAC", "103", "AC-3"} {
		if !strings.Contains(got, quiero) {
			t.Errorf("el informe de contenido no menciona %q: %q", quiero, got)
		}
	}

	// Y se informa UNA sola vez: repetirlo cada diez segundos sería ruido.
	if segunda := a.contents(); segunda != "" {
		t.Errorf("el contenido se ha informado dos veces: %q", segunda)
	}
}

// Mientras no haya llegado la PMT no se informa a medias: más vale callar que
// decir que un canal no lleva audio porque su tabla aún no ha pasado.
func TestContentsWaitsForThePMT(t *testing.T) {
	a := newTSAnalyzer()
	a.feed(mkTS(paq{pid: patPID, cc: 0, payload: true, pusi: true,
		datos: conPointer(patSection(1, 4096))}))
	if got := a.contents(); got != "" {
		t.Errorf("se ha informado del contenido con solo la PAT: %q", got)
	}
}

// ─── Robustez y ciclo de vida ────────────────────────────────────────────────

// Esto parsea lo que llega de la red. Un flujo corrupto, truncado o
// malintencionado no puede tumbar el relé: el trabajo del proceso es reenviar,
// y un panic aquí se llevaría el canal por delante.
func TestAnalyzerSurvivesGarbage(t *testing.T) {
	a := newTSAnalyzer()
	basura := [][]byte{
		{},
		{0x47},
		{0x47, 0xFF},
		make([]byte, tsPacket),
		bytesRepeat(0xFF, tsPacket),
		bytesRepeat(0x47, tsPacket*3),
		// Secciones que declaran longitudes imposibles.
		mkTS(paq{pid: patPID, payload: true, pusi: true, datos: []byte{0x00, 0x00, 0x0F, 0xFF}}),
		mkTS(paq{pid: patPID, payload: true, pusi: true, datos: []byte{0x00, 0x00, 0xB0, 0x00}}),
		mkTS(paq{pid: patPID, payload: true, pusi: true, datos: []byte{0xFF}}),
		// Campo de adaptación que se pasa del paquete.
		func() []byte { p := mkTS(paq{pid: 100, payload: true, af: true}); p[4] = 200; return p }(),
	}
	for i, b := range basura {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("basura %d ha provocado un panic: %v", i, r)
				}
			}()
			a.feed(b)
			a.snapshot()
			a.contents()
		}()
	}
}

func bytesRepeat(b byte, n int) []byte {
	s := make([]byte, n)
	for i := range s {
		s[i] = b
	}
	return s
}

// snapshot pone a cero los contadores del intervalo, pero NO el estado que da
// continuidad entre intervalos. Si se reiniciara el contador de cada PID, el
// primer paquete después de cada informe parecería un error y el log se
// llenaría de fallos inventados cada diez segundos.
func TestSnapshotResetsCountersButNotStreamState(t *testing.T) {
	a := newTSAnalyzer()
	a.feed(mkTS(paq{pid: 100, cc: 0, payload: true}))
	a.feed(mkTS(paq{pid: 100, cc: 5, payload: true})) // salto: 1 error

	if got := a.snapshot(); got == "" {
		t.Fatal("el primer intervalo debería avisar del salto")
	}
	// El intervalo siguiente continúa la secuencia correctamente desde el 5.
	a.feed(mkTS(paq{pid: 100, cc: 6, payload: true}))
	a.feed(mkTS(paq{pid: 100, cc: 7, payload: true}))
	if got := a.snapshot(); got != "" {
		t.Errorf("el segundo intervalo inventa errores: %q", got)
	}
}

// Un canal sano no imprime nada: un log que dice "todo bien" cada diez segundos
// es un log que nadie lee, y entonces tampoco se lee el que dice algo.
func TestHealthySilence(t *testing.T) {
	a := newTSAnalyzer()
	for i := 0; i < 100; i++ {
		a.feed(mkTS(paq{pid: 101, cc: uint8(i & 0x0F), payload: true,
			tienePCR: i%4 == 0, pcr: uint64(float64(i)*5*pcrPerMs) + 7}))
	}
	if got := a.snapshot(); got != "" {
		t.Errorf("un canal sano ha escrito en el log: %q", got)
	}
}

func TestTEIAndScramblingAreReported(t *testing.T) {
	a := newTSAnalyzer()
	a.feed(mkTS(paq{pid: 101, cc: 0, payload: true, tei: true}))
	a.feed(mkTS(paq{pid: 101, cc: 1, payload: true, scram: true}))
	got := a.snapshot()
	if !strings.Contains(got, "TEI") {
		t.Errorf("no se avisa del transport_error_indicator: %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "cifrado") && !strings.Contains(strings.ToLower(got), "scrambl") {
		t.Errorf("no se avisa de los paquetes cifrados: %q", got)
	}
}

// Un datagrama que no trae paquetes enteros de 188 bytes es el fallo que deja
// un flujo ilegible con todas las estadísticas en verde.
func TestMisalignedDatagramsAreReported(t *testing.T) {
	a := newTSAnalyzer()
	d := make([]byte, tsPacket+50)
	copy(d, mkTS(paq{pid: 101, cc: 0, payload: true}))
	a.feed(d)
	if got := a.snapshot(); !strings.Contains(got, "188") {
		t.Errorf("no se avisa del datagrama mal alineado: %q", got)
	}
}

// El análisis va activo por defecto, así que su coste tiene que ser
// despreciable frente al trabajo de reenviar. Un datagrama clásico son 7
// paquetes TS; a 10 Mbps eso es ~950 datagramas por segundo y canal.
// El estado se calienta ANTES de medir, con PAT y PMT ya parseadas: la primera
// versión de este benchmark medía con el mapa de programas vacío, cosa que deja
// de ser cierta en cuanto pasa la primera tabla, y daba una cifra optimista.
func BenchmarkAnalyzerFeed(b *testing.B) {
	a := newTSAnalyzer()
	a.feed(mkTS(paq{pid: patPID, cc: 0, payload: true, pusi: true,
		datos: conPointer(patSection(1, 4096))}))
	a.feed(mkTS(paq{pid: 4096, cc: 0, payload: true, pusi: true,
		datos: conPointer(pmtSection(101, map[uint16]uint8{101: 0x1B, 102: 0x0F, 103: 0x81}))}))

	// Un datagrama como los de verdad: 7 paquetes, mezcla de PID, uno con PCR y
	// uno nulo, que es lo que trae un CBR.
	d := make([]byte, 0, 7*tsPacket)
	pids := []uint16{101, 101, 102, 101, nullPID, 103, 101}
	cc := map[uint16]uint8{}
	for i, pid := range pids {
		d = append(d, mkTS(paq{pid: pid, cc: cc[pid], payload: true,
			tienePCR: i == 0, pcr: uint64(i)*1000 + 7})...)
		cc[pid] = (cc[pid] + 1) & 0x0F
	}
	b.SetBytes(int64(len(d)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.feed(d)
	}
}

// ─── Regresiones de la revisión adversarial ──────────────────────────────────

// Un canal de pago va cifrado por definición. Con aviso por nivel escribía
// "66.489 paquetes cifrados" cada diez segundos para siempre, en la MISMA línea
// donde salen los errores de continuidad — así que a la semana el operador
// filtra "TS:" y el analizador entero deja de existir.
func TestScramblingWarnsOnceNotForever(t *testing.T) {
	a := newTSAnalyzer()
	// El contador tiene que seguir correlativo ENTRE intervalos: si se
	// reiniciara en cada tanda, el propio test fabricaría un error de
	// continuidad en cada frontera y no probaría nada de lo que dice.
	cc := uint8(0)
	emitir := func() {
		for i := 0; i < 200; i++ {
			a.feed(mkTS(paq{pid: 101, cc: cc, payload: true, scram: true}))
			cc = (cc + 1) & 0x0F
		}
	}
	emitir()
	if got := a.snapshot(); !strings.Contains(strings.ToLower(got), "cifrado") &&
		!strings.Contains(strings.ToLower(got), "scrambl") {
		t.Fatalf("el primer intervalo debería avisar del cifrado: %q", got)
	}
	for i := 0; i < 3; i++ {
		emitir()
		if got := a.snapshot(); got != "" {
			t.Fatalf("intervalo %d de un canal de pago sano escribe en el log: %q", i+2, got)
		}
	}
}

// El contador de PCR de 33 bits da la vuelta cada ~26,5 h. Es aritmética
// modular, no una discontinuidad: sin tratarlo, todo canal escribía un aviso de
// salto de reloj cada veintiséis horas y media sin que pasara nada.
func TestPCRCounterWrapIsNotAJump(t *testing.T) {
	a := newTSAnalyzer()
	// Justo antes de la vuelta, y 20 ms después de ella.
	antes := uint64(pcrModulus) - uint64(10*pcrPerMs) + 1
	despues := uint64(10 * pcrPerMs)
	a.feed(mkTS(paq{pid: 100, cc: 0, payload: true, tienePCR: true, pcr: antes}))
	a.feed(mkTS(paq{pid: 100, cc: 1, payload: true, tienePCR: true, pcr: despues}))

	if got := a.snapshot(); got != "" {
		t.Errorf("la vuelta del contador se ha tomado por un fallo: %q", got)
	}
}

// Un paquete corrupto gasta su hueco en el contador igual. Ignorarlo a secas
// hacía que el siguiente paquete bueno llegara dos por delante y se contara
// como pérdida: un solo suceso producía dos avisos, uno inventado.
func TestTEIDoesNotInventAContinuityError(t *testing.T) {
	a := newTSAnalyzer()
	a.feed(mkTS(paq{pid: 101, cc: 0, payload: true}))
	a.feed(mkTS(paq{pid: 101, cc: 1, payload: true, tei: true})) // corrupto
	a.feed(mkTS(paq{pid: 101, cc: 2, payload: true}))            // el siguiente bueno
	a.feed(mkTS(paq{pid: 101, cc: 3, payload: true}))

	if a.ccErrs != 0 {
		t.Errorf("%d errores de continuidad inventados alrededor de un paquete con TEI", a.ccErrs)
	}
}

// Un empalme declarado —publicidad, ventana regional, un encoder que
// rearranca— deja el reloj de la PAT anclado antes del salto, y la siguiente
// PAT medía el salto entero como si la tabla hubiera desaparecido: un aviso de
// Prioridad 1 de treinta segundos en cada corte publicitario.
func TestDeclaredSpliceDoesNotInventAPATFailure(t *testing.T) {
	a := newTSAnalyzer()
	pcr := func(ms float64) uint64 { return uint64(ms*pcrPerMs) + 1 }
	ccPat := uint8(0)
	pat := func() []byte {
		p := mkTS(paq{pid: patPID, cc: ccPat, payload: true, pusi: true,
			datos: conPointer(patSection(1, 4096))})
		ccPat = (ccPat + 1) & 0x0F
		return p
	}
	// Flujo normal: PCR cada 20 ms, PAT por medio.
	for i := 0; i < 5; i++ {
		a.feed(mkTS(paq{pid: 100, cc: uint8(i), payload: true, tienePCR: true, pcr: pcr(float64(i) * 20)}))
		a.feed(pat())
	}
	// Empalme DECLARADO de +30 s, y el flujo sigue.
	a.feed(mkTS(paq{pid: 100, cc: 5, payload: true, tienePCR: true, pcr: pcr(30100), discont: true}))
	for i := 0; i < 5; i++ {
		a.feed(pat())
		a.feed(mkTS(paq{pid: 100, cc: uint8(6 + i), payload: true, tienePCR: true,
			pcr: pcr(30120 + float64(i)*20)}))
	}

	if got := a.snapshot(); got != "" {
		t.Errorf("un empalme declarado ha inventado un fallo: %q", got)
	}
}

// La comprobación de PAT medía el hueco ENTRE dos tablas, así que si la PAT
// desaparecía del todo no se medía nada y no se avisaba — justo lo que la
// comprobación existe para ver.
func TestVanishedPATIsReported(t *testing.T) {
	a := newTSAnalyzer()
	pcr := func(ms float64) uint64 { return uint64(ms*pcrPerMs) + 1 }
	a.feed(mkTS(paq{pid: patPID, cc: 0, payload: true, pusi: true,
		datos: conPointer(patSection(1, 4096))}))
	// Y a partir de aquí, dos segundos de flujo sin una sola PAT.
	for i := 0; i < 100; i++ {
		a.feed(mkTS(paq{pid: 100, cc: uint8(i & 0x0F), payload: true,
			tienePCR: true, pcr: pcr(float64(i) * 20)}))
	}
	got := a.snapshot()
	if !strings.Contains(got, "PAT") {
		t.Errorf("la PAT ha desaparecido y no se avisa: %q", got)
	}
}

// Un datagrama RTP con UN solo paquete TS mide 12+188 = 200 bytes exactos. Con
// un `>` en vez de `>=` se rechazaba entero, y un canal emitido así se
// informaba como "no es MPEG-TS" de principio a fin.
func TestSingleTSPacketOverRTPIsRecognised(t *testing.T) {
	d := make([]byte, rtpHeaderLen+tsPacket)
	d[0], d[1] = 0x80, rtpPayloadTypeMP2T
	copy(d[rtpHeaderLen:], mkTS(paq{pid: 101, cc: 0, payload: true}))

	if got := payloadOf(d); len(got) != tsPacket {
		t.Fatalf("RTP con un paquete TS: devuelve %d bytes, quiero %d", len(got), tsPacket)
	}
	a := newTSAnalyzer()
	a.feed(d)
	if a.notTS != 0 {
		t.Errorf("un datagrama RTP válido se ha contado como basura")
	}
}

// La PAT viene de la red y no se le verifica el CRC, así que una corrupta puede
// declarar hasta 65.535 programas. Antes cada uno se guardaba y maybePMT
// barría el mapa entero por CADA paquete con payload: entrada de red que
// degrada el bucle de reenvío.
func TestProgramMapIsBounded(t *testing.T) {
	a := newTSAnalyzer()
	for n := 1; n <= 3000; n++ {
		a.feed(mkTS(paq{pid: patPID, cc: uint8(n & 0x0F), payload: true, pusi: true,
			datos: conPointer(patSection(uint16(n), uint16(4096+n%100)))}))
	}
	if len(a.programs) > maxPrograms {
		t.Errorf("el mapa de programas ha crecido a %d desde la red (tope %d)", len(a.programs), maxPrograms)
	}
}

// Un programa cuya PMT no llega no puede silenciar el informe de los demás.
func TestContentsReportsTheProgrammesItKnows(t *testing.T) {
	a := newTSAnalyzer()
	// Dos programas en la PAT, PMT de uno solo.
	a.feed(mkTS(paq{pid: patPID, cc: 0, payload: true, pusi: true,
		datos: conPointer(patSection(1, 4096))}))
	a.feed(mkTS(paq{pid: patPID, cc: 1, payload: true, pusi: true,
		datos: conPointer(patSection(2, 4097))}))
	a.feed(mkTS(paq{pid: 4096, cc: 0, payload: true, pusi: true,
		datos: conPointer(pmtSection(101, map[uint16]uint8{101: 0x1B}))}))

	got := a.contents()
	if !strings.Contains(got, "H.264") {
		t.Errorf("el programa con PMT conocida no se informa: %q", got)
	}
}
