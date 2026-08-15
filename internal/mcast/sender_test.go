package mcast

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sendNames(es []SendCfg) string {
	var n []string
	for _, e := range es {
		n = append(n, e.Name)
	}
	return strings.Join(n, ",")
}

func TestParseBitrate(t *testing.T) {
	ok := map[string]int{
		"1000":  1000,
		"512k":  512_000,
		"10M":   10_000_000,
		"2.5M":  2_500_000,
		" 8m ":  8_000_000,
		"1G":    1_000_000_000,
		"1500K": 1_500_000,
	}
	for in, want := range ok {
		got, err := parseBitrate(in)
		if err != nil || got != want {
			t.Errorf("parseBitrate(%q) = %d, %v; quiero %d", in, got, err, want)
		}
	}
	// Lo que TIENE que fallar. Los tres últimos son los peligrosos: se truncan
	// o desbordan a un bitrate de 0 o negativo, y con eso el emisor se queda sin
	// reloj y manda a velocidad de cable.
	for _, in := range []string{
		"", "abc", "-5", "0", "M", "10X",
		"0.5",  // quien piensa en megas y no pone sufijo: truncaría a 0 bps
		"0.99", // ídem
		"1e19", // desborda int64 y sale negativo
		"1e10G",
		"Inf", "NaN",
	} {
		if got, err := parseBitrate(in); err == nil {
			t.Errorf("parseBitrate(%q) = %d sin error; tiene que rechazarse", in, got)
		}
	}
}

// Con un bitrate inservible el emisor no puede arrancar: sin reloj, el bucle
// emitiría tan rápido como acepte la pila y saturaría el segmento.
func TestSenderRefusesToRunWithoutAClock(t *testing.T) {
	useLang(t, langEN)
	for _, cfg := range []SendCfg{
		{Name: "a", Dest: "239.0.10.1:5000", Bitrate: 0, Size: 1316},
		{Name: "a", Dest: "239.0.10.1:5000", Bitrate: -1, Size: 1316},
		{Name: "a", Dest: "239.0.10.1:5000", Bitrate: 1_000_000, Size: 0},
	} {
		err := runSender(context.Background(), cfg, &stats{name: "a"}, log.New(io.Discard, "", 0))
		if err == nil {
			t.Errorf("bitrate=%d size=%d: arrancó igual", cfg.Bitrate, cfg.Size)
		}
	}
}

func sendCfg(ch SendChannelCfg) SendConfig {
	return SendConfig{Channels: []SendChannelCfg{ch}}
}

func TestSenderRejectsUnusableChannels(t *testing.T) {
	useLang(t, langEN)
	dir := t.TempDir()
	real := filepath.Join(dir, "x.ts")
	if err := os.WriteFile(real, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		nombre string
		ch     SendChannelCfg
		aviso  string
	}{
		{"sin destino", SendChannelCfg{Name: "a"}, "has no dest"},
		{"destino inválido", SendChannelCfg{Name: "a", Dest: "basura"}, "invalid destination"},
		{"destino sin puerto", SendChannelCfg{Name: "a", Dest: "239.0.10.1:0"}, "no port"},
		{"bitrate inválido", SendChannelCfg{Name: "a", Dest: "239.0.10.1:5000", Bitrate: "diez megas"}, "invalid bitrate"},
		{"tamaño inservible", SendChannelCfg{Name: "a", Dest: "239.0.10.1:5000", Size: 4}, "unusable datagram size"},
		{"fichero y stdin", SendChannelCfg{Name: "a", Dest: "239.0.10.1:5000", File: real, Stdin: true}, "more than one source"},
		{"fichero y exec", SendChannelCfg{Name: "a", Dest: "239.0.10.1:5000", File: real, Exec: "ffmpeg -i x -f mpegts -"}, "more than one source"},
		{"exec inexistente", SendChannelCfg{Name: "a", Dest: "239.0.10.1:5000", Exec: "no-existe-este-programa-xyz -f -"}, "cannot run"},
		{"exec vacío", SendChannelCfg{Name: "a", Dest: "239.0.10.1:5000", Exec: "   "}, "cannot run"},
		{"fichero que no existe", SendChannelCfg{Name: "a", Dest: "239.0.10.1:5000", File: filepath.Join(dir, "no")}, "cannot read"},
		{"ttl fuera de rango", SendChannelCfg{Name: "a", Dest: "239.0.10.1:5000", TTL: ptrInt(300)}, "out of range"},
	}
	for _, c := range cases {
		r := resolveSendChannels(sendCfg(c.ch), 10)
		if len(r.channels) != 0 {
			t.Errorf("%s: aceptado", c.nombre)
		}
		if !hasWarn(r.warns, c.aviso) {
			t.Errorf("%s: aviso = %v, esperaba algo con %q", c.nombre, r.warns, c.aviso)
		}
	}
}

func ptrInt(v int) *int { return &v }

// Dos canales al mismo destino entrelazarían sus flujos y el receptor vería un
// transport stream corrupto imposible de diagnosticar.
func TestSenderRejectsTwoChannelsToTheSameDestination(t *testing.T) {
	useLang(t, langEN)
	c := SendConfig{Channels: []SendChannelCfg{
		{Name: "a", Dest: "239.0.10.1:5000"},
		{Name: "b", Dest: "239.0.10.1:5000"},
	}}

	r := resolveSendChannels(c, 10)

	if sendNames(r.channels) != "a" {
		t.Fatalf("canales = %q, quiero solo \"a\"", sendNames(r.channels))
	}
	if !hasWarn(r.warns, "already used by another channel") {
		t.Fatalf("sin aviso del destino repetido: %v", r.warns)
	}
}

func TestSenderDefaultsAndOverrides(t *testing.T) {
	loopback := false
	c := SendConfig{
		Defaults: SendDefaults{
			Iface: "10.30.0.5", TTL: 3, Loopback: &loopback,
			Sndbuf: 4096, Size: 1316, Bitrate: "10M",
		},
		Channels: []SendChannelCfg{
			{Name: "hereda", Dest: "239.0.10.1:5000"},
			{Name: "sobrescribe", Dest: "239.0.10.2:5000", Bitrate: "512k", Size: 940, TTL: ptrInt(7)},
		},
	}

	r := resolveSendChannels(c, 10)

	if len(r.channels) != 2 {
		t.Fatalf("canales = %d: %v", len(r.channels), r.warns)
	}
	a, b := r.channels[0], r.channels[1] // ordenados por nombre
	if a.Name != "hereda" || a.Bitrate != 10_000_000 || a.Size != 1316 || a.TTL != 3 ||
		a.Iface != "10.30.0.5" || a.Loopback || a.Sndbuf != 4096 {
		t.Errorf("el canal que hereda no recoge los defaults: %+v", a)
	}
	if b.Bitrate != 512_000 || b.Size != 940 || b.TTL != 7 {
		t.Errorf("el canal que sobrescribe no aplica lo suyo: %+v", b)
	}
}

// El nombre automático sale del destino, no de la posición: insertar un canal
// no puede renombrar y reiniciar a los demás.
func TestSenderAutomaticNameComesFromDest(t *testing.T) {
	r := resolveSendChannels(sendCfg(SendChannelCfg{Dest: "239.0.10.1:5000"}), 10)
	if len(r.channels) != 1 || r.channels[0].Name != "239.0.10.1:5000" {
		t.Fatalf("nombre automático = %q", sendNames(r.channels))
	}
}

func TestSenderFlagsModeMatchesDaemonValidation(t *testing.T) {
	useLang(t, langEN)
	// Un destino imposible tiene que rechazarse igual que en el JSON.
	r := resolveSendChannels(sendConfigFromFlags("basura", "", "", false, "10M", 1316, "", 8, true, 0), 10)
	if len(r.channels) != 0 {
		t.Fatal("destino inválido aceptado en modo flags")
	}

	// Y uno bueno tiene que conservar las opciones.
	r = resolveSendChannels(sendConfigFromFlags("239.0.10.1:5000", "", "", true, "512k", 940, "10.30.0.5", 3, false, 1<<16), 10)
	if len(r.channels) != 1 {
		t.Fatalf("canal válido rechazado: %v", r.warns)
	}
	e := r.channels[0]
	if !e.Stdin || e.Bitrate != 512_000 || e.Size != 940 || e.TTL != 3 || e.Loopback || e.Sndbuf != 1<<16 {
		t.Fatalf("opciones no respetadas: %+v", e)
	}
}

// El patrón tiene que ser numerado y consecutivo: es lo que permite detectar
// pérdidas y duplicados en el otro extremo.
func TestPatternSourceIsSequential(t *testing.T) {
	var p patternSource
	buf := make([]byte, 64)
	// Se lee igual que lo hace el receptor de la prueba extremo a extremo: si
	// aquí se reconstruyera el número "a mano" el test pasaría aunque el
	// formato cambiara, y el receptor dejaría de entenderlo sin que nadie lo
	// notara.
	for want := uint64(0); want < 5; want++ {
		n, err := p.next(buf)
		if err != nil || n != len(buf) {
			t.Fatalf("next = %d, %v", n, err)
		}
		if got := binary.BigEndian.Uint64(buf[:8]); got != want {
			t.Fatalf("secuencia = %d, quiero %d", got, want)
		}
	}
	// Y el número tiene que seguir siendo legible en la frontera de un byte.
	p.seq = 254
	for _, want := range []uint64{254, 255, 256, 257} {
		p.next(buf)
		if got := binary.BigEndian.Uint64(buf[:8]); got != want {
			t.Fatalf("secuencia = %d, quiero %d", got, want)
		}
	}
}

// Un canal que agota su fuente ha TERMINADO, no se ha caído: si el Manager lo
// tomara por una caída, reabriría el fichero cada pocos segundos y un
// "-loop-file=false" acabaría emitiendo en bucle igualmente.
func TestExhaustedSourceFinishesInsteadOfRestarting(t *testing.T) {
	m := testSendManager(t)
	m.retryDelay = 10 * time.Millisecond
	var calls int32
	m.run = func(ctx context.Context, e SendCfg, st *stats) error {
		atomic.AddInt32(&calls, 1)
		return errSourceDone
	}

	m.apply([]SendCfg{{Name: "a", Dest: "239.0.10.1:5000", Bitrate: 1e6, Size: 1316}})

	select {
	case <-m.Drained():
	case <-time.After(3 * time.Second):
		t.Fatal("el canal terminado no se retira: el proceso no saldría nunca")
	}
	time.Sleep(100 * time.Millisecond) // margen por si se reintentara
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("la fuente se abrió %d veces; con la fuente agotada tiene que abrirse una sola", n)
	}
}

// Y una caída de verdad sí tiene que reintentarse.
func TestRealFailureStillRetries(t *testing.T) {
	m := testSendManager(t)
	m.retryDelay = 10 * time.Millisecond
	var calls int32
	m.run = func(ctx context.Context, e SendCfg, st *stats) error {
		if atomic.AddInt32(&calls, 1) < 3 {
			return errors.New("la NIC ha desaparecido")
		}
		<-ctx.Done()
		return nil
	}

	m.apply([]SendCfg{{Name: "a", Dest: "239.0.10.1:5000", Bitrate: 1e6, Size: 1316}})
	deadline := time.After(3 * time.Second)
	for atomic.LoadInt32(&calls) < 3 {
		select {
		case <-deadline:
			t.Fatalf("solo %d intentos: una caída de verdad tiene que reintentarse", calls)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func testSendManager(t *testing.T) *Manager[SendCfg] {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := NewManager[SendCfg](ctx, log.New(io.Discard, "", 0), log.New(io.Discard, "", 0))
	m.sender = true
	m.run = func(ctx context.Context, e SendCfg, st *stats) error { <-ctx.Done(); return nil }
	t.Cleanup(m.shutdown)
	return m
}

// Conservar a ciegas un canal rechazado reintroduce por la puerta de atrás lo
// que la validación acaba de rechazar. El escenario: el operador intercambia
// los destinos de dos canales y se equivoca al escribir el segundo.
func TestKeptChannelCannotCollideWithAnAcceptedOne(t *testing.T) {
	useLang(t, langEN)
	m := testSendManager(t)

	viejoA := SendCfg{Name: "a", Dest: "239.0.10.1:5000", Bitrate: 1e6, Size: 1316}
	viejoB := SendCfg{Name: "b", Dest: "239.0.20.1:5000", Bitrate: 1e6, Size: 1316}
	m.apply([]SendCfg{viejoA, viejoB})

	// Recarga: "a" pasa a emitir al destino que tenía "b", y "b" no valida
	// (bitrate ilegible, por ejemplo). Conservar "b" tal cual lo pondría a
	// emitir al mismo grupo que "a".
	nuevoA := SendCfg{Name: "a", Dest: "239.0.20.1:5000", Bitrate: 1e6, Size: 1316}
	desired := m.keepRunning([]SendCfg{nuevoA}, map[string]bool{"b": true}, sendCompatible)

	if len(desired) != 1 {
		t.Fatalf("canales = %s: se ha conservado uno que emite al mismo destino que otro",
			sendNames(desired))
	}
	if desired[0].Dest != "239.0.20.1:5000" {
		t.Fatalf("el canal que queda no es el que validó: %+v", desired[0])
	}
}

// Y si no choca, se conserva como siempre: el arreglo no puede volverse
// paranoico y tirar canales que sí podían seguir.
func TestKeptChannelSurvivesWhenItDoesNotCollide(t *testing.T) {
	useLang(t, langEN)
	m := testSendManager(t)

	m.apply([]SendCfg{
		{Name: "a", Dest: "239.0.10.1:5000", Bitrate: 1e6, Size: 1316},
		{Name: "b", Dest: "239.0.20.1:5000", Bitrate: 1e6, Size: 1316},
	})

	// "b" se rechaza pero su destino de siempre no lo usa nadie más.
	desired := m.keepRunning(
		[]SendCfg{{Name: "a", Dest: "239.0.10.1:5000", Bitrate: 1e6, Size: 1316}},
		map[string]bool{"b": true}, sendCompatible)

	if sendNames(desired) != "a,b" {
		t.Fatalf("canales = %q, quiero conservar también \"b\"", sendNames(desired))
	}
}

// ─── Formatos que no pueden viajar por UDP ───────────────────────────────────

func TestDetectContainer(t *testing.T) {
	dir := t.TempDir()
	escribe := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		relleno := append(b, make([]byte, 64)...)
		if err := os.WriteFile(p, relleno, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	casos := map[string][]byte{
		"MP4/MOV":       append([]byte{0, 0, 0, 0x20}, []byte("ftypisom")...),
		"Matroska/WebM": {0x1A, 0x45, 0xDF, 0xA3},
		"AVI":           append([]byte("RIFF\x00\x00\x00\x00"), []byte("AVI ")...),
		"MPEG-PS (VOB)": {0x00, 0x00, 0x01, 0xBA},
		"ASF/WMV":       {0x30, 0x26, 0xB2, 0x75},
	}
	i := 0
	for want, magic := range casos {
		i++
		if got := detectContainer(escribe(fmt.Sprintf("f%d.bin", i), magic)); got != want {
			t.Errorf("detectContainer(%s) = %q, quiero %q", want, got, want)
		}
	}
	// Un TS y un binario cualquiera no son contenedores reconocibles: se emiten.
	if got := detectContainer(tsFile(t, 4, 0)); got != "" {
		t.Errorf("un TS se ha tomado por %q", got)
	}
	if got := detectContainer(escribe("raw.bin", []byte{1, 2, 3, 4})); got != "" {
		t.Errorf("un binario cualquiera se ha tomado por %q", got)
	}
}

// Un MP4 emitido a pelo salía a su bitrate, con cero errores, y no lo leía
// nadie. Ahora se rechaza y se da la orden exacta para arreglarlo.
func TestRejectsNonTSContainerWithARemuxHint(t *testing.T) {
	useLang(t, langEN)
	dir := t.TempDir()
	mp4 := filepath.Join(dir, "peli.mp4")
	body := append(append([]byte{0, 0, 0, 0x20}, []byte("ftypisom")...), make([]byte, 4096)...)
	if err := os.WriteFile(mp4, body, 0o644); err != nil {
		t.Fatal(err)
	}

	r := resolveSendChannels(sendCfg(SendChannelCfg{Name: "a", Dest: "239.0.10.1:5000", File: mp4}), 10)

	if len(r.channels) != 0 {
		t.Fatal("un MP4 aceptado: emitiría basura sin que nadie lo note")
	}
	aviso := strings.Join(r.warns, " ")
	for _, quiero := range []string{"MP4/MOV", "MPEG-TS", "ffmpeg -i", "-c copy -f mpegts", "peli.ts"} {
		if !strings.Contains(aviso, quiero) {
			t.Errorf("el aviso no contiene %q:\n%s", quiero, aviso)
		}
	}
}

// ─── Orden externa como fuente ───────────────────────────────────────────────

func TestSplitCommand(t *testing.T) {
	casos := []struct {
		in   string
		want []string
	}{
		{"ffmpeg -i x.mp4 -f mpegts -", []string{"ffmpeg", "-i", "x.mp4", "-f", "mpegts", "-"}},
		{`ffmpeg -i "/srv/con espacios/x.mp4" -c copy`, []string{"ffmpeg", "-i", "/srv/con espacios/x.mp4", "-c", "copy"}},
		{"  ffmpeg   -i    x   ", []string{"ffmpeg", "-i", "x"}},
		{`echo 'una cosa'`, []string{"echo", "una cosa"}},
		{`echo ""`, []string{"echo", ""}}, // argumento vacío explícito
		{"", nil},
		{"   ", nil},
	}
	for _, c := range casos {
		got := splitCommand(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitCommand(%q) = %q, quiero %q", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitCommand(%q)[%d] = %q, quiero %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// helperCommand se llama a sí mismo: es la forma portable de tener una orden
// externa de verdad en un test, sin depender de que exista cat, head o ffmpeg.
func helperCommand(bytes, exit int) string {
	return fmt.Sprintf("%s -test.run=TestExecHelperProcess -- %d %d", os.Args[0], bytes, exit)
}

func TestExecHelperProcess(t *testing.T) {
	args := os.Args
	var rest []string
	for i, a := range args {
		if a == "--" {
			rest = args[i+1:]
			break
		}
	}
	if len(rest) != 2 {
		t.Skip("no es una invocación de ayuda")
	}
	n, _ := strconv.Atoi(rest[0])
	code, _ := strconv.Atoi(rest[1])
	os.Stdout.Write(make([]byte, n))
	if code != 0 {
		fmt.Fprintln(os.Stderr, "el material no se puede abrir")
		os.Exit(code)
	}
	os.Exit(0)
}

func TestExecSourceReadsTheCommandOutput(t *testing.T) {
	src, err := startExec(context.Background(), helperCommand(4096, 0))
	if err != nil {
		t.Fatalf("arrancando la orden: %v", err)
	}
	defer src.Close()

	buf := make([]byte, 1316)
	total := 0
	for {
		n, err := src.next(buf)
		total += n
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("leyendo: %v", err)
		}
	}
	if total != 4096 {
		t.Fatalf("leídos %d bytes de 4096", total)
	}
}

// Si la orden se cae, el canal tiene que caerse con ella Y decir por qué: sin
// la cola de su salida de error, se ve el canal reintentarse cada 3 s sin una
// sola pista de qué le pasa.
func TestExecSourceReportsWhyTheCommandDied(t *testing.T) {
	useLang(t, langEN)
	src, err := startExec(context.Background(), helperCommand(512, 3))
	if err != nil {
		t.Fatalf("arrancando la orden: %v", err)
	}
	defer src.Close()

	buf := make([]byte, 1316)
	var last error
	for i := 0; i < 10; i++ {
		if _, last = src.next(buf); last != nil {
			break
		}
	}
	if last == nil || last == io.EOF {
		t.Fatalf("una orden que sale con código 3 tiene que dar error, dio %v", last)
	}
	if !strings.Contains(last.Error(), "el material no se puede abrir") {
		t.Fatalf("el error no incluye la salida de la orden: %v", last)
	}
}

// ─── Alineación de transport stream ──────────────────────────────────────────

// tsFile escribe un fichero con pinta de TS: 0x47 al principio de cada paquete
// de 188 bytes. extra añade bytes de más para desalinear el tamaño total.
func tsFile(t *testing.T, packets, extra int) string {
	t.Helper()
	buf := make([]byte, packets*tsPacket+extra)
	for i := 0; i < packets; i++ {
		buf[i*tsPacket] = 0x47
	}
	path := filepath.Join(t.TempDir(), "x.ts")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLooksLikeTS(t *testing.T) {
	if !looksLikeTS(tsFile(t, 4, 0)) {
		t.Error("un TS bien formado no se reconoce")
	}
	// Un fichero cualquiera no puede confundirse con un TS.
	otro := filepath.Join(t.TempDir(), "x.bin")
	if err := os.WriteFile(otro, make([]byte, 4*tsPacket), 0o644); err != nil {
		t.Fatal(err)
	}
	if looksLikeTS(otro) {
		t.Error("un fichero de ceros se toma por TS")
	}
	if looksLikeTS(filepath.Join(t.TempDir(), "no-existe")) {
		t.Error("un fichero que no existe no puede parecer TS")
	}
}

// Un tamaño que no es múltiplo de 188 parte los paquetes TS entre datagramas:
// el flujo sale a su bitrate, con cero errores, y ningún decodificador lo lee.
func TestWarnsWhenDatagramSizeBreaksTSAlignment(t *testing.T) {
	useLang(t, langEN)
	path := tsFile(t, 40, 0)

	r := resolveSendChannels(sendCfg(SendChannelCfg{
		Name: "a", Dest: "239.0.10.1:5000", File: path, Size: 1400, // 1400 = 7,44 paquetes
	}), 10)

	if len(r.channels) != 1 {
		t.Fatalf("el canal se ha rechazado, y esto solo debe ser un aviso: %v", r.warns)
	}
	if !hasWarn(r.warns, "not a multiple") {
		t.Fatalf("sin aviso de alineación: %v", r.warns)
	}
	// Y con 1316 (7 × 188) no puede avisar de nada.
	r = resolveSendChannels(sendCfg(SendChannelCfg{
		Name: "a", Dest: "239.0.10.1:5000", File: path, Size: 1316,
	}), 10)
	if len(r.warns) != 0 {
		t.Fatalf("1316 es múltiplo de 188 y aun así avisa: %v", r.warns)
	}
}

// Si el fichero no mide un número entero de paquetes, cada vuelta del bucle
// emite uno cortado y el decodificador ve una discontinuidad.
func TestWarnsWhenFileLengthBreaksTSAlignment(t *testing.T) {
	useLang(t, langEN)

	r := resolveSendChannels(sendCfg(SendChannelCfg{
		Name: "a", Dest: "239.0.10.1:5000", File: tsFile(t, 40, 7),
	}), 10)

	if len(r.channels) != 1 {
		t.Fatalf("el canal se ha rechazado, y esto solo debe ser un aviso: %v", r.warns)
	}
	if !hasWarn(r.warns, "truncated packet") {
		t.Fatalf("sin aviso de longitud: %v", r.warns)
	}
}

// Y el aviso no puede saltar con material que no es TS: emitir otra cosa con el
// tamaño que sea es un uso legítimo.
func TestNoTSWarningForNonTSMaterial(t *testing.T) {
	useLang(t, langEN)
	path := filepath.Join(t.TempDir(), "x.bin")
	if err := os.WriteFile(path, []byte(strings.Repeat("no soy un TS", 500)), 0o644); err != nil {
		t.Fatal(err)
	}

	r := resolveSendChannels(sendCfg(SendChannelCfg{
		Name: "a", Dest: "239.0.10.1:5000", File: path, Size: 1400,
	}), 10)

	if len(r.warns) != 0 {
		t.Fatalf("avisa de alineación TS con material que no es TS: %v", r.warns)
	}
}

// El ejemplo que se publica en el repo tiene que seguir siendo válido.
func TestSendExampleConfigIsValid(t *testing.T) {
	c, warns, err := loadSendConfig(filepath.Join("..", "..", "mcast-send.example.json"))
	if err != nil {
		t.Fatalf("no se puede leer el ejemplo: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("el ejemplo usa campos que el programa no conoce: %v", warns)
	}

	r := resolveSendChannels(c, 999)

	// El canal con fichero se rechaza si la ruta no existe, cosa normal en un
	// ejemplo: lo que se comprueba aquí es que el resto valida y que el patrón
	// generado, que no depende de ningún fichero, sí arranca.
	if r.stats != 10 {
		t.Errorf("intervalo de stats = %v, quiero el 10 del JSON", r.stats)
	}
	var patron *SendCfg
	for i := range r.channels {
		if r.channels[i].Name == "patron-pruebas" {
			patron = &r.channels[i]
		}
	}
	if patron == nil {
		t.Fatalf("el canal de patrón del ejemplo no valida: %v", r.warns)
	}
	if patron.Bitrate != 2_000_000 || patron.Size != 1316 || patron.TTL != 8 {
		t.Errorf("el canal de patrón no hereda bien: %+v", patron)
	}
}

// El fichero se repite sin fin cuando loop está activo, y termina cuando no.
func TestFileLoopRepeatsAndStops(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.bin")
	if err := os.WriteFile(path, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}

	enBucle, err := openFileLoop(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer enBucle.Close()
	buf := make([]byte, 4)
	var leido int
	for i := 0; i < 10; i++ { // más vueltas que bytes tiene el fichero
		n, err := enBucle.next(buf)
		if err != nil {
			t.Fatalf("en bucle no debería terminar nunca: %v", err)
		}
		leido += n
	}
	if leido < 20 {
		t.Fatalf("leídos %d bytes en 10 vueltas: no está repitiendo", leido)
	}

	unaVez, err := openFileLoop(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer unaVez.Close()
	var fin bool
	for i := 0; i < 10; i++ {
		if _, err := unaVez.next(buf); err != nil {
			fin = true
			break
		}
	}
	if !fin {
		t.Fatal("sin bucle, el fichero tiene que agotarse")
	}
}
