package mcast

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// ─── Análisis del transport stream que pasa por el canal ─────────────────────
//
// Las estadísticas dicen CUÁNTO pasa, nunca QUÉ pasa. `err` son fallos de envío
// y `drop` son datagramas de otro grupo: un TS puede venir roto —errores de
// continuidad, PCR con saltos, PAT que desaparece— con los dos contadores a
// cero y el relé informando de que todo va bien.
//
// Sin esto no se puede distinguir "el relé va bien" de "la señal va bien", que
// es justo lo que hay que demostrar cuando llama el cliente. Con esto, la
// respuesta a "el canal 12 se ve a saltos" deja de ser un encogimiento de
// hombros y pasa a ser "PID 101, 47 errores de continuidad en cinco minutos":
// viene de arriba, no de aquí.
//
// Lo que se mira es un subconjunto de las Prioridades 1 y 2 de ETSI TR 101 290,
// el que se puede comprobar sin decodificar nada.

const (
	nullPID = 0x1FFF
	patPID  = 0

	// maxPCRGap es el intervalo máximo entre PCR consecutivos que admite la
	// norma (TR 101 290 §5.2.1). Por encima, el reloj del decodificador va a la
	// deriva entre referencias.
	maxPCRGap = 40 // ms

	// maxPATGap es el intervalo máximo entre PAT. Sin PAT no hay forma de saber
	// qué programas lleva el flujo, así que su ausencia es Prioridad 1.
	maxPATGap = 500 // ms

	// pcrPerMs son las unidades de PCR que caben en un milisegundo.
	pcrPerMs = pcrClockHz / 1000
)

// pidState es lo que hay que recordar de cada PID entre paquete y paquete.
type pidState struct {
	packets uint64
	ccErrs  uint64
	// lastCC es el último contador de continuidad visto, y haveCC dice si ya
	// hay uno con el que comparar: el primer paquete de un PID nunca es un
	// error.
	lastCC uint8
	haveCC bool
	// lastPayload guarda si el paquete anterior traía payload. El contador solo
	// avanza en los que lo llevan, así que sin este dato no se puede saber si
	// una repetición es legítima.
	dupSeen bool
	// streamType sale de la PMT; 0 = todavía no se sabe.
	streamType uint8
}

// tsAnalyzer acumula el estado del flujo. Vive en la goroutine de recepción y
// se consulta desde la de estadísticas, así que lleva su propio candado: es uno
// por datagrama, no por paquete TS, y sin contención medible.
type tsAnalyzer struct {
	mu sync.Mutex

	pids map[uint16]*pidState

	// Contadores del intervalo. Se ponen a cero en cada informe, igual que
	// hacen pkts/byts/errs: son sucesos, y como acumulado desde el arranque no
	// dirían si el problema es de ahora o de anteayer.
	packets, nulls    uint64
	teiErrs           uint64
	scrambled         uint64
	notTS, misaligned uint64
	ccErrs            uint64
	// worstPID es el PID con más errores de continuidad del intervalo: nombrar
	// uno concreto es lo que convierte el aviso en accionable.
	worstPID  uint16
	worstErrs uint64

	// Reloj. lastPCR es la última referencia vista en el PID que la lleva.
	pcrPID      uint16
	havePCR     bool
	lastPCR     uint64
	pcrCount    uint64
	pcrMaxGapMs float64
	pcrOver     uint64 // intervalos por encima de maxPCRGap
	pcrJumps    uint64 // saltos sin discontinuity_indicator
	pcrExtZero  uint64 // referencias con la extensión de 27 MHz a cero

	// PAT medida en el reloj del propio flujo, no en el de pared: así el dato
	// es el del flujo y no el de la red que lo trae, y no hace falta una
	// llamada al reloj por datagrama.
	patCount    uint64
	patLastPCR  uint64
	havePatPCR  bool
	patMaxGapMs float64
	patOver     uint64

	// Contenido, tal como lo declara la PMT.
	programs map[uint16]*program
	// reported dice si ya se ha registrado el contenido: es una línea de log de
	// una sola vez, no algo que repetir cada diez segundos.
	reported bool
	// Estas dos describen el flujo, no un suceso del intervalo, así que se
	// avisan por flanco. Sobreviven al reinicio de contadores de snapshot.
	noPCRAvisado bool
	noExtAvisado bool
}

type program struct {
	number  uint16
	pmtPID  uint16
	pcrPID  uint16
	streams map[uint16]uint8 // PID -> stream_type
}

func newTSAnalyzer() *tsAnalyzer {
	return &tsAnalyzer{
		pids:     make(map[uint16]*pidState),
		programs: make(map[uint16]*program),
	}
}

// payloadOf devuelve la parte del datagrama que es transport stream, saltando
// la cabecera RTP si la lleva.
//
// El relé reenvía lo que le llega, y por multicast profesional viaja tanto TS a
// pelo como TS dentro de RTP. Si no se reconociera el RTP, todo flujo
// encapsulado se contaría como basura y el analizador sería ruido.
func payloadOf(d []byte) []byte {
	if len(d) >= tsPacket && d[0] == 0x47 {
		return d
	}
	// RTP: versión 2 en los dos bits altos, y detrás tiene que empezar un
	// paquete TS. Solo se reconoce la cabecera fija de 12 bytes sin CSRC ni
	// extensión, que es lo que usa MPEG-TS sobre RTP (RFC 2250).
	if len(d) > rtpHeaderLen+tsPacket && d[0]&0xC0 == 0x80 && d[0]&0x0F == 0 &&
		d[0]&0x10 == 0 && d[rtpHeaderLen] == 0x47 {
		return d[rtpHeaderLen:]
	}
	return nil
}

// feed analiza un datagrama recibido.
func (a *tsAnalyzer) feed(datagram []byte) {
	ts := payloadOf(datagram)

	a.mu.Lock()
	defer a.mu.Unlock()

	if ts == nil {
		a.notTS++
		return
	}
	if len(ts)%tsPacket != 0 {
		// Datagrama que no cuadra con paquetes enteros: los TS salen partidos
		// entre datagramas y ningún decodificador puede leer el flujo. Se
		// analiza lo que cabe entero y se deja constancia.
		a.misaligned++
	}
	for off := 0; off+tsPacket <= len(ts); off += tsPacket {
		a.packet(ts[off : off+tsPacket])
	}
}

func (a *tsAnalyzer) packet(p []byte) {
	if p[0] != 0x47 {
		a.notTS++
		return
	}
	a.packets++

	if p[1]&0x80 != 0 { // transport_error_indicator
		a.teiErrs++
		// Un paquete marcado como corrupto no dice nada fiable de su contenido:
		// contarlo como salto de continuidad sería duplicar el mismo suceso.
		return
	}
	pid := uint16(p[1]&0x1F)<<8 | uint16(p[2])
	if pid == nullPID {
		a.nulls++
		return // los nulos no llevan contador de continuidad definido
	}
	if p[3]&0xC0 != 0 {
		a.scrambled++
	}

	st := a.pids[pid]
	if st == nil {
		st = &pidState{}
		a.pids[pid] = st
	}
	st.packets++

	afc := (p[3] >> 4) & 0x03
	if afc == 0 {
		return // valor reservado: ni payload ni campo de adaptación
	}
	tieneAF := afc == 2 || afc == 3
	tienePayload := afc == 1 || afc == 3

	// discontinuity_indicator: el flujo avisa de que el contador va a saltar.
	// Hacerle caso es lo que evita marcar como error cada empalme legítimo.
	discontinuo := tieneAF && p[4] > 0 && p[5]&0x80 != 0

	a.continuity(pid, st, p[3]&0x0F, tienePayload, discontinuo)

	if tieneAF && p[4] > 0 {
		a.clock(pid, p, discontinuo)
	}
	if pid == patPID && tienePayload {
		a.pat(p)
	} else if tienePayload {
		a.maybePMT(pid, p)
	}
}

// continuity aplica las reglas del contador de continuidad, que son menos
// obvias de lo que parecen: solo avanza en los paquetes CON payload, la norma
// permite UN duplicado exacto, y una discontinuidad declarada no es un error.
// Contar cualquier repetición como fallo llenaría el log de falsos positivos en
// cualquier flujo real.
func (a *tsAnalyzer) continuity(pid uint16, st *pidState, cc uint8, tienePayload, discontinuo bool) {
	if !st.haveCC {
		st.lastCC, st.haveCC = cc, true
		return
	}
	esperado := st.lastCC
	if tienePayload {
		esperado = (st.lastCC + 1) & 0x0F
	}
	switch {
	case cc == esperado:
		st.dupSeen = false
	case cc == st.lastCC && tienePayload && !st.dupSeen:
		// Un duplicado exacto está permitido, pero solo uno seguido.
		st.dupSeen = true
		return
	case discontinuo:
		// Salto anunciado: se acepta y se re-sincroniza.
		st.dupSeen = false
	default:
		st.ccErrs++
		a.ccErrs++
		st.dupSeen = false
		if st.ccErrs > a.worstErrs {
			a.worstErrs, a.worstPID = st.ccErrs, pid
		}
	}
	st.lastCC = cc
}

// clock sigue el PCR: cada cuánto llega, si pega saltos sin avisar, y si trae
// la extensión de 27 MHz. Sin esa extensión el reloj queda cuantizado a 90 kHz
// —11,1 µs—, veintidós veces por encima de la tolerancia de PCR_accuracy, y no
// hay forma de que el flujo sea conforme por mucho que se ajuste lo demás.
func (a *tsAnalyzer) clock(pid uint16, p []byte, discontinuo bool) {
	v, ok := pcrOf(p)
	if !ok {
		return
	}
	if a.havePCR && pid != a.pcrPID {
		return // otro programa: el reloj que se sigue es el del primero visto
	}
	// El recuento y la comprobación de la extensión van ANTES de separar el
	// primer PCR del resto. Contando solo a partir del segundo, "todas las
	// referencias sin extensión" nunca llegaba a cumplirse en el primer
	// intervalo y el aviso no salía.
	a.pcrCount++
	if v%300 == 0 {
		a.pcrExtZero++
	}
	if !a.havePCR {
		a.havePCR, a.pcrPID, a.lastPCR = true, pid, v
		return
	}
	d := int64(v) - int64(a.lastPCR)
	a.lastPCR = v
	if d <= 0 || d > maxPCRJump {
		if !discontinuo {
			// Salto sin bandera: o el contador de 33 bits ha dado la vuelta
			// —cada ~26 h, y entonces debería venir declarado— o alguien ha
			// empalmado material sin avisar. Es lo que descoloca el PLL del
			// decodificador.
			a.pcrJumps++
		}
		return // el intervalo de un salto no mide nada
	}
	ms := float64(d) / pcrPerMs
	if ms > a.pcrMaxGapMs {
		a.pcrMaxGapMs = ms
	}
	if ms > maxPCRGap {
		a.pcrOver++
	}
}

// section devuelve la sección PSI de un paquete que la empieza, si cabe entera
// en él.
//
// Reensamblar secciones repartidas entre paquetes es otro trabajo; para PAT y
// para las PMT de un servicio normal no hace falta, porque caben de sobra. Lo
// que NO se hace es adivinar: una sección que no cabe se ignora, y eso solo
// significa que de ese flujo no se informa del contenido, nunca que se informe
// mal.
func section(p []byte) []byte {
	if p[1]&0x40 == 0 { // payload_unit_start_indicator
		return nil
	}
	off := 4
	if (p[3]>>4)&0x02 != 0 { // hay campo de adaptación
		off += 1 + int(p[4])
	}
	if off >= tsPacket {
		return nil
	}
	off += 1 + int(p[off]) // pointer_field
	if off+3 > tsPacket {
		return nil
	}
	s := p[off:]
	// section_length son los 12 bits bajos de los bytes 1 y 2, y cuenta desde
	// el byte 3.
	n := int(s[1]&0x0F)<<8 | int(s[2])
	if n+3 > len(s) {
		return nil // repartida entre paquetes
	}
	return s[:n+3]
}

func (a *tsAnalyzer) pat(p []byte) {
	// El intervalo se mide en el reloj del flujo, no en el de pared: así el
	// dato describe el flujo y no la red que lo trae.
	if a.havePCR {
		if a.havePatPCR {
			if d := int64(a.lastPCR) - int64(a.patLastPCR); d > 0 {
				ms := float64(d) / pcrPerMs
				if ms > a.patMaxGapMs {
					a.patMaxGapMs = ms
				}
				if ms > maxPATGap {
					a.patOver++
				}
			}
		}
		a.patLastPCR, a.havePatPCR = a.lastPCR, true
	}
	a.patCount++

	s := section(p)
	// La longitud se comprueba siempre: esto parsea lo que llega de la red, y
	// un flujo corrupto o malintencionado no puede tumbar el relé. Tampoco se
	// verifica el CRC, así que una sección corrupta puede colar una entrada
	// falsa; es un informe, no una decisión, y se corrige sola en la siguiente.
	if s == nil || len(s) < 12 || s[0] != 0x00 { // table_id de la PAT
		return
	}
	// Cabecera de 8 bytes, cuerpo de entradas de 4, y 4 de CRC al final.
	cuerpo := s[8 : len(s)-4]
	for i := 0; i+4 <= len(cuerpo); i += 4 {
		num := uint16(cuerpo[i])<<8 | uint16(cuerpo[i+1])
		pmt := uint16(cuerpo[i+2]&0x1F)<<8 | uint16(cuerpo[i+3])
		if num == 0 {
			continue // program_number 0 es la NIT, no un programa
		}
		if _, ya := a.programs[num]; !ya {
			a.programs[num] = &program{number: num, pmtPID: pmt, streams: map[uint16]uint8{}}
		}
	}
}

func (a *tsAnalyzer) maybePMT(pid uint16, p []byte) {
	var pr *program
	for _, x := range a.programs {
		if x.pmtPID == pid {
			pr = x
			break
		}
	}
	if pr == nil {
		return
	}
	s := section(p)
	// 12 de cabecera hasta program_info_length más 4 de CRC es el mínimo con el
	// que se puede leer algo sin salirse.
	if s == nil || len(s) < 16 || s[0] != 0x02 { // table_id de la PMT
		return
	}
	pr.pcrPID = uint16(s[8]&0x1F)<<8 | uint16(s[9])
	infoLen := int(s[10]&0x0F)<<8 | int(s[11])
	off := 12 + infoLen
	fin := len(s) - 4 // sin el CRC
	for off+5 <= fin {
		tipo := s[off]
		ePID := uint16(s[off+1]&0x1F)<<8 | uint16(s[off+2])
		esLen := int(s[off+3]&0x0F)<<8 | int(s[off+4])
		pr.streams[ePID] = tipo
		if st := a.pids[ePID]; st != nil {
			st.streamType = tipo
		}
		off += 5 + esLen
	}
}

// codecName traduce el stream_type de la PMT. Solo los que se ven en
// distribución: para el resto vale más el número que una etiqueta inventada.
func codecName(t uint8) string {
	switch t {
	case 0x01, 0x02:
		return "MPEG-2"
	case 0x03, 0x04:
		return "MP2"
	case 0x0F:
		return "AAC"
	case 0x11:
		return "AAC-LATM"
	case 0x1B:
		return "H.264"
	case 0x24:
		return "H.265"
	case 0x06:
		return "privado" // AC-3/subtítulos en DVB: lo dice el descriptor
	case 0x81:
		return "AC-3"
	case 0x87:
		return "E-AC-3"
	}
	return fmt.Sprintf("tipo 0x%02X", t)
}

// contents describe lo que lleva el flujo, una sola vez, cuando ya se ha visto
// la PMT. Devuelve "" mientras no haya nada firme que decir.
func (a *tsAnalyzer) contents() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.reported || len(a.programs) == 0 {
		return ""
	}
	nums := make([]int, 0, len(a.programs))
	for n := range a.programs {
		nums = append(nums, int(n))
	}
	sort.Ints(nums)

	var partes []string
	for _, n := range nums {
		pr := a.programs[uint16(n)]
		if len(pr.streams) == 0 {
			return "" // aún no ha llegado su PMT: se espera en vez de informar a medias
		}
		pids := make([]int, 0, len(pr.streams))
		for p := range pr.streams {
			pids = append(pids, int(p))
		}
		sort.Ints(pids)
		var comp []string
		for _, p := range pids {
			comp = append(comp, fmt.Sprintf("%d %s", p, codecName(pr.streams[uint16(p)])))
		}
		partes = append(partes, fmt.Sprintf(txt.logTSProgram, n, pr.pcrPID, strings.Join(comp, " · ")))
	}
	a.reported = true
	return strings.Join(partes, "; ")
}

// snapshot devuelve lo anómalo del intervalo y pone los contadores a cero.
// Cadena vacía significa que no hay nada que contar, y entonces no se imprime
// una línea: un log que dice "todo bien" cada diez segundos es un log que nadie
// lee.
func (a *tsAnalyzer) snapshot() string {
	a.mu.Lock()
	defer a.mu.Unlock()

	var av []string
	if a.notTS > 0 {
		av = append(av, fmt.Sprintf(txt.logTSNotTS, a.notTS))
	}
	if a.misaligned > 0 {
		av = append(av, fmt.Sprintf(txt.logTSMisaligned, a.misaligned))
	}
	if a.ccErrs > 0 {
		av = append(av, fmt.Sprintf(txt.logTSContinuity, a.ccErrs, a.worstPID))
	}
	if a.teiErrs > 0 {
		av = append(av, fmt.Sprintf(txt.logTSTEI, a.teiErrs))
	}
	if a.pcrJumps > 0 {
		av = append(av, fmt.Sprintf(txt.logTSPCRJump, a.pcrJumps))
	}
	if a.pcrOver > 0 {
		av = append(av, fmt.Sprintf(txt.logTSPCRGap, a.pcrOver, a.pcrMaxGapMs, maxPCRGap))
	}
	if a.patOver > 0 {
		av = append(av, fmt.Sprintf(txt.logTSPATGap, a.patOver, a.patMaxGapMs, maxPATGap))
	}
	// Estas dos no son sucesos, son propiedades del flujo: mientras el encoder
	// no cambie, siguen siendo ciertas para siempre. Se avisa en el FLANCO, la
	// primera vez que se cumplen, y se vuelve a armar el aviso cuando dejan de
	// cumplirse — así una recaída se ve, pero un canal que siempre ha sido así
	// no repite la misma línea cada diez segundos hasta que nadie lee el log.
	sinPCR := a.packets > 0 && a.pcrCount == 0
	if sinPCR && !a.noPCRAvisado {
		av = append(av, txt.logTSNoPCR)
	}
	if a.packets > 0 {
		a.noPCRAvisado = sinPCR
	}
	sinExt := a.pcrCount > 0 && a.pcrExtZero == a.pcrCount
	if sinExt && !a.noExtAvisado {
		av = append(av, txt.logTSPCRNoExt)
	}
	if a.pcrCount > 0 {
		a.noExtAvisado = sinExt
	}
	if a.scrambled > 0 {
		av = append(av, fmt.Sprintf(txt.logTSScrambled, a.scrambled))
	}

	// Los contadores del intervalo se reinician; el estado que da continuidad
	// entre intervalos (contadores de PID, reloj, programas) NO, porque
	// perderlo haría que el primer paquete de cada intervalo pareciera un
	// error.
	a.packets, a.nulls = 0, 0
	a.teiErrs, a.scrambled = 0, 0
	a.notTS, a.misaligned = 0, 0
	a.ccErrs, a.worstErrs, a.worstPID = 0, 0, 0
	a.pcrCount, a.pcrMaxGapMs, a.pcrOver, a.pcrJumps, a.pcrExtZero = 0, 0, 0, 0, 0
	a.patCount, a.patMaxGapMs, a.patOver = 0, 0, 0
	for _, st := range a.pids {
		st.ccErrs = 0
	}

	return strings.Join(av, " · ")
}
