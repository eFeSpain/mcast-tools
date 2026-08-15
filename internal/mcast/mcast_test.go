package mcast

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func chanNames(es []EffCfg) string {
	var n []string
	for _, e := range es {
		n = append(n, e.Name)
	}
	return strings.Join(n, ",")
}

func hasWarn(warns []string, substr string) bool {
	for _, w := range warns {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

// useLang fija el idioma de los mensajes durante un test: si no, las
// aserciones sobre el texto dependen del idioma de la máquina que compila.
func useLang(t *testing.T, l lang) {
	t.Helper()
	prev := txt
	txt = messagesFor(l)
	t.Cleanup(func() { txt = prev })
}

// ─── Protección contra bucles de realimentación ──────────────────────────────

func TestRejectsChannelWithDestEqualToItsSource(t *testing.T) {
	useLang(t, langEN)
	c := Config{Channels: []ChannelCfg{{
		Name:   "a",
		Source: "239.0.10.1:5000",
		Dest:   []string{"239.255.0.1:1234", "239.0.10.1:5000"},
	}}}

	r := resolveChannels(c, 10)

	if len(r.channels) != 0 {
		t.Fatalf("canal que se realimenta aceptado: %s", chanNames(r.channels))
	}
	if !hasWarn(r.warns, "feedback loop") {
		t.Fatalf("no avisa del bucle: %v", r.warns)
	}
}

// El socket RX se bindea al comodín, así que un destino unicast dirigido a esta
// misma máquina en un puerto de origen vuelve a entrar por él: realimenta igual
// que apuntar al propio grupo, y antes se colaba sin aviso.
func TestRejectsUnicastDestThatFeedsBackIntoOurOwnPort(t *testing.T) {
	useLang(t, langEN)
	local := []net.IP{net.ParseIP("10.30.0.5").To4()}

	for _, dst := range []string{"127.0.0.1:5000", "10.30.0.5:5000", "255.255.255.255:5000", "0.0.0.0:5000"} {
		c := Config{Channels: []ChannelCfg{
			{Name: "a", Source: "239.0.10.1:5000", Dest: []string{dst}},
		}}

		r := resolveChannelsWith(c, 10, local)

		if len(r.channels) != 0 {
			t.Errorf("destino %s aceptado: realimenta nuestro propio puerto de recepción", dst)
		}
		if !hasWarn(r.warns, "feedback loop") {
			t.Errorf("destino %s: sin aviso de bucle (%v)", dst, r.warns)
		}
	}
}

// Y no puede pasarse de listo: otra máquina en el mismo puerto, o esta máquina
// en un puerto que no es origen de nada, son destinos legítimos.
func TestAllowsUnicastDestThatDoesNotFeedBack(t *testing.T) {
	local := []net.IP{net.ParseIP("10.30.0.5").To4()}

	for _, dst := range []string{"10.30.0.9:5000", "10.30.0.5:1234"} {
		c := Config{Channels: []ChannelCfg{
			{Name: "a", Source: "239.0.10.1:5000", Dest: []string{dst}},
		}}

		r := resolveChannelsWith(c, 10, local)

		if len(r.channels) != 1 {
			t.Errorf("destino legítimo %s rechazado: %v", dst, r.warns)
		}
	}
}

// El idioma elegido tiene que llegar hasta los avisos de validación, no solo
// hasta la ayuda.
func TestWarningsFollowTheSelectedLanguage(t *testing.T) {
	bad := Config{Channels: []ChannelCfg{
		{Name: "a", Source: "10.30.0.5:5000", Dest: []string{"239.255.0.1:1234"}},
	}}

	useLang(t, langES)
	if r := resolveChannels(bad, 10); !hasWarn(r.warns, "no es una dirección multicast") {
		t.Errorf("en español: %v", r.warns)
	}

	useLang(t, langEN)
	if r := resolveChannels(bad, 10); !hasWarn(r.warns, "is not a multicast address") {
		t.Errorf("en inglés: %v", r.warns)
	}
}

func TestRejectsCycleBetweenChannels(t *testing.T) {
	useLang(t, langEN)
	c := Config{Channels: []ChannelCfg{
		{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"239.0.20.1:5000"}},
		{Name: "b", Source: "239.0.20.1:5000", Dest: []string{"239.0.10.1:5000"}},
	}}

	r := resolveChannels(c, 10)

	if chanNames(r.channels) != "a" {
		t.Fatalf("canales aceptados = %q, quiero solo \"a\"", chanNames(r.channels))
	}
	if !hasWarn(r.warns, "feedback loop") {
		t.Fatalf("no avisa del bucle: %v", r.warns)
	}
}

func TestAllowsCascadeBetweenChannels(t *testing.T) {
	// a alimenta al grupo que lee b: es una cascada legítima, no un bucle.
	c := Config{Channels: []ChannelCfg{
		{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"239.0.20.1:5000"}},
		{Name: "b", Source: "239.0.20.1:5000", Dest: []string{"239.0.30.1:5000"}},
	}}

	r := resolveChannels(c, 10)

	if chanNames(r.channels) != "a,b" {
		t.Fatalf("canales aceptados = %q, quiero \"a,b\" (avisos: %v)", chanNames(r.channels), r.warns)
	}
}

// ─── Validación de direcciones y parámetros ──────────────────────────────────

func TestRejectsNonMulticastSource(t *testing.T) {
	c := Config{Channels: []ChannelCfg{
		{Name: "uni", Source: "10.30.0.5:5000", Dest: []string{"239.255.0.1:1234"}},
	}}

	r := resolveChannels(c, 10)

	if len(r.channels) != 0 {
		t.Fatalf("source unicast aceptado: %s", chanNames(r.channels))
	}
	if !hasWarn(r.warns, "multicast") {
		t.Fatalf("no avisa de que el source no es multicast: %v", r.warns)
	}
}

func TestAllowsUnicastDest(t *testing.T) {
	// Reenviar a un receptor unicast es un uso válido.
	c := Config{Channels: []ChannelCfg{
		{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"10.30.0.9:1234"}},
	}}

	r := resolveChannels(c, 10)

	if chanNames(r.channels) != "a" {
		t.Fatalf("destino unicast rechazado (avisos: %v)", r.warns)
	}
}

// Un TTL que el kernel rechaza dejaría el socket con TTL 1 mientras el log de
// arranque presume del valor pedido: mejor no arrancar el canal.
func TestRejectsTTLOutOfRange(t *testing.T) {
	useLang(t, langEN)
	for _, ttl := range []int{-1, 256, 300} {
		c := Config{Channels: []ChannelCfg{
			{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"239.255.0.1:1234"}, TTL: &ttl},
		}}

		r := resolveChannels(c, 10)

		if len(r.channels) != 0 {
			t.Errorf("ttl %d aceptado", ttl)
		}
		if !hasWarn(r.warns, "out of range") {
			t.Errorf("ttl %d: sin aviso (%v)", ttl, r.warns)
		}
	}
	// Y los extremos válidos siguen pasando.
	for _, ttl := range []int{0, 255} {
		c := Config{Channels: []ChannelCfg{
			{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"239.255.0.1:1234"}, TTL: &ttl},
		}}
		if r := resolveChannels(c, 10); len(r.channels) != 1 {
			t.Errorf("ttl %d rechazado: %v", ttl, r.warns)
		}
	}
}

func TestRejectsDuplicateChannelName(t *testing.T) {
	useLang(t, langEN)
	c := Config{Channels: []ChannelCfg{
		{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"239.255.0.1:1234"}},
		{Name: "a", Source: "239.0.20.1:5000", Dest: []string{"239.255.1.1:1234"}},
	}}

	r := resolveChannels(c, 10)

	if len(r.channels) != 1 {
		t.Fatalf("canales = %d, quiero 1: el segundo 'a' debe descartarse", len(r.channels))
	}
	if !hasWarn(r.warns, "duplicate channel") {
		t.Fatalf("el canal perdido no se avisa: %v", r.warns)
	}
}

// Un destino repetido emitiría cada paquete dos veces al mismo sitio.
func TestDeduplicatesDestinations(t *testing.T) {
	useLang(t, langEN)
	c := Config{Channels: []ChannelCfg{{
		Name:   "a",
		Source: "239.0.10.1:5000",
		Dest:   []string{"239.255.0.1:1234", "239.255.0.1:1234"},
	}}}

	r := resolveChannels(c, 10)

	if len(r.channels) != 1 || len(r.channels[0].Dest) != 1 {
		t.Fatalf("destinos = %v, quiero uno solo", r.channels)
	}
	if !hasWarn(r.warns, "repeats destination") {
		t.Fatalf("el destino repetido no se avisa: %v", r.warns)
	}
}

// Reordenar los destinos en el JSON no puede reiniciar el canal.
func TestReorderingDestinationsDoesNotChangeTheKey(t *testing.T) {
	mk := func(dest ...string) EffCfg {
		c := Config{Channels: []ChannelCfg{
			{Name: "a", Source: "239.0.10.1:5000", Dest: dest},
		}}
		r := resolveChannels(c, 10)
		if len(r.channels) != 1 {
			t.Fatalf("config no válida: %v", r.warns)
		}
		return r.channels[0]
	}

	uno := mk("239.255.0.1:1234", "239.255.1.1:1234")
	otro := mk("239.255.1.1:1234", "239.255.0.1:1234")

	if uno.key() != otro.key() {
		t.Fatalf("reordenar los destinos cambia la key y reiniciaría el canal:\n%s\n%s", uno.key(), otro.key())
	}
}

// El nombre automático no puede depender de la posición en el array: si
// dependiera, insertar un canal renombraría a los siguientes y el SIGHUP los
// reiniciaría todos.
func TestAutomaticNameIsStableAgainstInsertions(t *testing.T) {
	sinNombre := func(src string) ChannelCfg {
		return ChannelCfg{Source: src, Dest: []string{"239.255.0.1:1234"}}
	}

	antes := resolveChannels(Config{Channels: []ChannelCfg{
		sinNombre("239.0.10.1:5000"), sinNombre("239.0.20.1:5001"),
	}}, 10)
	despues := resolveChannels(Config{Channels: []ChannelCfg{
		sinNombre("239.0.30.1:5002"), sinNombre("239.0.10.1:5000"), sinNombre("239.0.20.1:5001"),
	}}, 10)

	name := func(r resolved, src string) string {
		for _, e := range r.channels {
			if e.Source == src {
				return e.Name
			}
		}
		return "(ausente)"
	}
	for _, src := range []string{"239.0.10.1:5000", "239.0.20.1:5001"} {
		if a, d := name(antes, src), name(despues, src); a != d {
			t.Errorf("%s se llamaba %q y tras insertar un canal delante pasa a %q", src, a, d)
		}
	}
}

// ─── SSM: filtro por emisor ──────────────────────────────────────────────────

func TestRejectsInvalidSourceFilter(t *testing.T) {
	useLang(t, langEN)
	// Un grupo, una dirección sin sentido y 0.0.0.0 no son emisores válidos.
	for _, from := range []string{"239.1.2.3", "no-es-una-ip", "0.0.0.0"} {
		c := Config{Channels: []ChannelCfg{{
			Name: "a", Source: "232.1.2.3:5000",
			Dest: []string{"239.255.0.1:1234"}, From: []string{from},
		}}}

		r := resolveChannels(c, 10)

		if len(r.channels) != 0 {
			t.Errorf("from=%q aceptado", from)
		}
		if !hasWarn(r.warns, "invalid source filter") {
			t.Errorf("from=%q: sin aviso (%v)", from, r.warns)
		}
	}
}

func TestKeepsSourceFilterCanonicalAndSorted(t *testing.T) {
	c := Config{Channels: []ChannelCfg{{
		Name: "a", Source: "232.1.2.3:5000",
		Dest: []string{"239.255.0.1:1234"},
		From: []string{" 10.20.30.41 ", "10.20.30.40"},
	}}}

	r := resolveChannels(c, 10)

	if len(r.channels) != 1 {
		t.Fatalf("rechazado: %v", r.warns)
	}
	got := r.channels[0].From
	if len(got) != 2 || got[0] != "10.20.30.40" || got[1] != "10.20.30.41" {
		t.Fatalf("from = %q, quiero [10.20.30.40 10.20.30.41]", got)
	}
}

func TestAllowedSender(t *testing.T) {
	from := []string{"10.20.30.40", "10.20.30.41"}
	cases := []struct {
		ip   string
		want bool
	}{
		{"10.20.30.40", true},
		{"10.20.30.41", true},
		{"10.20.30.42", false}, // el vecino de al lado no vale
		{"10.20.30.4", false},  // ni un prefijo del permitido
	}
	for _, c := range cases {
		src := &net.UDPAddr{IP: net.ParseIP(c.ip), Port: 5000}
		if got := allowedSender(from, src); got != c.want {
			t.Errorf("allowedSender(%s) = %v, quiero %v", c.ip, got, c.want)
		}
	}
	// Un remitente que no es UDP, o sin IP, no puede colarse.
	if allowedSender(from, nil) {
		t.Error("un remitente nulo no puede admitirse")
	}
	if allowedSender(from, &net.UDPAddr{}) {
		t.Error("un remitente sin IP no puede admitirse")
	}
}

func TestRejectsDestWithoutHost(t *testing.T) {
	c := Config{Channels: []ChannelCfg{
		{Name: "a", Source: "239.0.10.1:5000", Dest: []string{":1234"}},
	}}

	if r := resolveChannels(c, 10); len(r.channels) != 0 {
		t.Fatalf("destino sin host aceptado: %+v", r.channels)
	}
}

// Los defaults tienen que llegar a cada canal: si esto se rompe, un relé sale
// por la NIC equivocada o con el loopback al revés y ningún test se entera.
func TestDefaultsPropagateToEveryChannel(t *testing.T) {
	loop := false
	wd := 12.0
	c := Config{
		Defaults: Defaults{
			Iface: "10.30.0.5", TTL: 0, Rcvbuf: 0, Sndbuf: 7,
			Loop: &loop, Watchdog: &wd,
		},
		Channels: []ChannelCfg{
			{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"239.255.0.1:1234"}},
		},
	}

	r := resolveChannels(c, 10)

	if len(r.channels) != 1 {
		t.Fatalf("canales = %d: %v", len(r.channels), r.warns)
	}
	e := r.channels[0]
	if e.Iface != "10.30.0.5" {
		t.Errorf("iface = %q, quiero la de defaults", e.Iface)
	}
	if e.TTL != defTTL {
		t.Errorf("ttl = %d, quiero el valor por defecto %d", e.TTL, defTTL)
	}
	if e.Rcvbuf != defRcvbuf {
		t.Errorf("rcvbuf = %d, quiero el valor por defecto %d", e.Rcvbuf, defRcvbuf)
	}
	if e.Loop {
		t.Error("loop = true: el canal no ha heredado el loop=false de defaults")
	}
	if e.Sndbuf != 7 {
		t.Errorf("sndbuf = %d, quiero 7", e.Sndbuf)
	}
	if e.Watchdog != 12*time.Second {
		t.Errorf("watchdog = %v, quiero 12s", e.Watchdog)
	}
}

func TestWatchdogDefaultsAndOverride(t *testing.T) {
	base := ChannelCfg{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"239.255.0.1:1234"}}

	r := resolveChannels(Config{Channels: []ChannelCfg{base}}, 10)
	if got := r.channels[0].Watchdog; got != seconds(defWatchdog) {
		t.Errorf("watchdog por defecto = %v, quiero %v", got, seconds(defWatchdog))
	}

	off := 0.0
	base.Watchdog = &off
	r = resolveChannels(Config{Channels: []ChannelCfg{base}}, 10)
	if got := r.channels[0].Watchdog; got != 0 {
		t.Errorf("watchdog = %v con 0 explícito, quiero desactivado", got)
	}
}

// Un valor disparatado no puede desbordar time.Duration y volverse negativo.
func TestSecondsClampsAbsurdValues(t *testing.T) {
	if got := seconds(1e10); got <= 0 {
		t.Errorf("seconds(1e10) = %v, no puede ser negativo ni cero", got)
	}
	if got := seconds(-5); got != 0 {
		t.Errorf("seconds(-5) = %v, quiero 0", got)
	}
}

// ─── Modo flags: mismas validaciones que el modo daemon ──────────────────────

func TestFlagsModeSplitsAndTrimsDestinations(t *testing.T) {
	cfg := configFromFlags("239.0.10.1:5000", " 239.255.0.1:1234 , 239.255.1.1:1234 ", "", "", 8, true, 4096, 0, defWatchdog)

	r := resolveChannels(cfg, 10)

	if len(r.channels) != 1 {
		t.Fatalf("canales = %d, quiero 1 (avisos: %v)", len(r.channels), r.warns)
	}
	d := r.channels[0].Dest
	if len(d) != 2 || d[0] != "239.255.0.1:1234" || d[1] != "239.255.1.1:1234" {
		t.Fatalf("destinos = %q, quiero [239.255.0.1:1234 239.255.1.1:1234]", d)
	}
}

func TestFlagsModeRejectsInvalidSourceInsteadOfPanicking(t *testing.T) {
	cfg := configFromFlags("basura", "239.255.0.1:1234", "", "", 8, true, 4096, 0, defWatchdog)

	r := resolveChannels(cfg, 10)

	if len(r.channels) != 0 {
		t.Fatalf("source inválido aceptado: %s", chanNames(r.channels))
	}
	if len(r.warns) == 0 {
		t.Fatal("source inválido sin aviso")
	}
}

func TestFlagsModeKeepsExplicitOptions(t *testing.T) {
	cfg := configFromFlags("239.0.10.1:5000", "239.255.0.1:1234", "", "10.30.0.5", 3, false, 1<<20, 1<<19, 30)

	r := resolveChannels(cfg, 10)

	if len(r.channels) != 1 {
		t.Fatalf("canales = %d, quiero 1", len(r.channels))
	}
	e := r.channels[0]
	if e.Iface != "10.30.0.5" || e.TTL != 3 || e.Loop != false || e.Rcvbuf != 1<<20 || e.Sndbuf != 1<<19 {
		t.Fatalf("opciones no respetadas: %+v", e)
	}
	if e.Watchdog != 30*time.Second {
		t.Fatalf("watchdog = %v, quiero 30s", e.Watchdog)
	}
}

// El ejemplo que se publica en el repo tiene que seguir siendo válido.
func TestExampleConfigIsValid(t *testing.T) {
	c, cfgWarns, err := loadConfig(filepath.Join("..", "..", "mcast-dup.example.json"))
	if err != nil {
		t.Fatalf("no se puede leer el ejemplo: %v", err)
	}
	if len(cfgWarns) != 0 {
		t.Fatalf("el ejemplo usa campos que el programa no conoce: %v", cfgWarns)
	}

	// Con un valor de flag distinto del que trae el JSON, para que la
	// comprobación del intervalo no sea tautológica.
	r := resolveChannels(c, 999)

	if len(r.warns) != 0 {
		t.Fatalf("el ejemplo produce avisos: %v", r.warns)
	}
	if len(r.channels) != 3 {
		t.Fatalf("canales = %d, quiero 3", len(r.channels))
	}
	if r.stats != 10 {
		t.Fatalf("intervalo de stats = %v, quiero el 10 del JSON", r.stats)
	}
	if r.channels[0].Iface != "10.30.0.5" || r.channels[0].TTL != 8 {
		t.Fatalf("el primer canal no hereda los defaults: %+v", r.channels[0])
	}
	if r.channels[2].TTL != 2 || r.channels[2].Iface != "10.31.0.5" {
		t.Fatalf("el tercer canal no aplica sus overrides: %+v", r.channels[2])
	}
}

// ─── Interfaz de red ─────────────────────────────────────────────────────────

func TestResolveIface(t *testing.T) {
	useLang(t, langEN)

	if ifi, err := resolveIface(""); ifi != nil || err != nil {
		t.Errorf("sin iface debería devolver nil,nil; devolvió %v,%v", ifi, err)
	}
	if _, err := resolveIface("interfaz-que-no-existe-xyz"); err == nil {
		t.Error("un nombre inexistente debería dar error")
	}
	// TEST-NET-3: no la tiene nadie configurada.
	if _, err := resolveIface("203.0.113.99"); err == nil {
		t.Error("una IP que no está en la máquina debería dar error")
	}

	// Y por nombre debería funcionar con una NIC real que sirva para multicast.
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skip("no se pueden enumerar las interfaces")
	}
	for _, ifi := range ifaces {
		if ifi.Flags&net.FlagUp != 0 && ifi.Flags&net.FlagMulticast != 0 {
			got, err := resolveIface(ifi.Name)
			if err != nil || got == nil || got.Name != ifi.Name {
				t.Errorf("resolveIface(%q) = %v, %v", ifi.Name, got, err)
			}
			return
		}
	}
	t.Skip("no hay ninguna interfaz operativa con multicast en esta máquina")
}

// ─── El fichero de log sobrevive a logrotate ─────────────────────────────────

func TestLogFileIsReopenedAfterRotation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows no deja renombrar un fichero abierto; logrotate es cosa de Unix")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "mcast.log")
	r := &reopenFile{path: path}
	if err := r.reopen(); err != nil {
		t.Fatalf("apertura inicial: %v", err)
	}
	defer r.Close()
	fmt.Fprintln(r, "antes-de-rotar")

	// Esto es lo que hace logrotate: mueve el fichero y espera que el proceso
	// reabra. Sin reabrir, el proceso seguiría escribiendo al inodo viejo.
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatalf("rotación: %v", err)
	}
	if err := r.reopen(); err != nil {
		t.Fatalf("reapertura: %v", err)
	}
	fmt.Fprintln(r, "despues-de-rotar")

	nuevo, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no se ha creado un fichero nuevo: %v", err)
	}
	if !strings.Contains(string(nuevo), "despues-de-rotar") {
		t.Errorf("lo escrito tras rotar no está en el fichero nuevo: %q", nuevo)
	}
	if strings.Contains(string(nuevo), "antes-de-rotar") {
		t.Errorf("el fichero nuevo trae lo de antes de rotar: %q", nuevo)
	}
}

// ─── Ciclo de vida de los canales ────────────────────────────────────────────

// testManager entrega un Manager cuyo relé es de mentira: los tests ejercitan
// el ciclo de vida sin abrir sockets ni dejar goroutines de red sueltas.
func testManager(t *testing.T) *Manager[EffCfg] {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := NewManager[EffCfg](ctx, log.New(io.Discard, "", 0), log.New(io.Discard, "", 0))
	m.run = func(ctx context.Context, e EffCfg, st *stats) error {
		<-ctx.Done()
		return nil
	}
	t.Cleanup(m.shutdown)
	return m
}

func fakeChannel(name string) EffCfg {
	return EffCfg{Name: name, Source: "239.0.10.1:5000", Dest: []string{"239.255.0.1:1234"}, TTL: 1}
}

// gatedWorker es un canal de mentira que solo suelta los sockets cuando el
// test lo decide. Sin relojes: si la espera se rompiera, el test se colgaría en
// vez de dar un veredicto distinto según lo cargada que esté la máquina.
type gatedWorker struct {
	w        *worker[EffCfg]
	release  chan struct{}
	released int32
}

func newGatedWorker() *gatedWorker {
	g := &gatedWorker{release: make(chan struct{})}
	done := make(chan struct{})
	g.w = &worker[EffCfg]{
		cfg: EffCfg{Name: "a"},
		key: "config-vieja",
		st:  &stats{name: "a"},
		cancel: func() {
			go func() {
				<-g.release
				atomic.StoreInt32(&g.released, 1)
				close(done)
			}()
		},
		done: done,
	}
	return g
}

// Si apply arranca el relevo antes de que el canal anterior cierre sus sockets,
// durante esa ventana el flujo sale duplicado.
func TestApplyWaitsForTheOldWorkerBeforeRestarting(t *testing.T) {
	m := testManager(t)
	g := newGatedWorker()
	m.wk["a"] = g.w

	finished := make(chan struct{})
	go func() {
		m.apply([]EffCfg{fakeChannel("a")})
		close(finished)
	}()

	// Mientras el canal viejo no suelte, apply no puede haber terminado.
	select {
	case <-finished:
		t.Fatal("apply terminó sin esperar a que el canal anterior soltara los sockets")
	case <-time.After(100 * time.Millisecond):
	}

	close(g.release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("apply no terminó tras liberarse el canal anterior")
	}
	if atomic.LoadInt32(&g.released) == 0 {
		t.Fatal("apply arrancó el relevo antes de que el canal anterior soltara los sockets")
	}
}

func TestApplyGivesUpWaitingForAStuckWorker(t *testing.T) {
	m := testManager(t)
	m.stopTimeout = 50 * time.Millisecond
	// cancel que no hace nada y done que nunca se cierra: worker atascado.
	m.wk["a"] = &worker[EffCfg]{cfg: EffCfg{Name: "a"}, key: "config-vieja", st: &stats{name: "a"},
		cancel: func() {}, done: make(chan struct{})}

	start := time.Now()
	finished := make(chan struct{})
	go func() {
		m.apply([]EffCfg{fakeChannel("a")})
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("apply se quedó colgado esperando a un canal atascado")
	}
	if waited := time.Since(start); waited < m.stopTimeout {
		t.Fatalf("apply devolvió en %v, antes del plazo de %v: no llegó a esperar", waited, m.stopTimeout)
	}
	// El plazo se restablece para que el shutdown del cleanup no abandone al
	// worker nuevo antes de que termine.
	m.stopTimeout = 2 * time.Second
}

func TestShutdownWaitsForWorkersToStop(t *testing.T) {
	m := testManager(t)
	g := newGatedWorker()
	m.wk["a"] = g.w

	finished := make(chan struct{})
	go func() {
		m.shutdown()
		close(finished)
	}()

	select {
	case <-finished:
		t.Fatal("shutdown devolvió antes de que los canales soltaran los sockets")
	case <-time.After(100 * time.Millisecond):
	}

	close(g.release)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown no terminó tras liberarse el canal")
	}
	if atomic.LoadInt32(&g.released) == 0 {
		t.Fatal("shutdown no esperó a que se soltaran los sockets")
	}
}

// Cuando se agota el plazo hay que nombrar a TODOS los que siguen pendientes,
// no solo a aquel en el que tocó esperar.
func TestStopTimeoutNamesEveryPendingChannel(t *testing.T) {
	useLang(t, langEN)
	var buf syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager[EffCfg](ctx, log.New(io.Discard, "", 0), log.New(&buf, "", 0))
	m.stopTimeout = 30 * time.Millisecond

	stuck := func(name string) *worker[EffCfg] {
		return &worker[EffCfg]{cfg: EffCfg{Name: name}, key: "vieja", st: &stats{name: name},
			cancel: func() {}, done: make(chan struct{})}
	}
	m.waitStopped([]*worker[EffCfg]{stuck("uno"), stuck("dos"), stuck("tres")})

	out := buf.String()
	for _, name := range []string{"uno", "dos", "tres"} {
		if !strings.Contains(out, name) {
			t.Errorf("el aviso no nombra a %q:\n%s", name, out)
		}
	}
}

// La promesa central del SIGHUP: lo que no cambia, no se toca.
func TestUnchangedChannelSurvivesReload(t *testing.T) {
	m := testManager(t)
	e := fakeChannel("a")
	m.apply([]EffCfg{e})
	before := m.wk["a"]

	m.apply([]EffCfg{e}) // misma config exacta

	if m.wk["a"] != before {
		t.Fatal("un canal sin cambios se ha reiniciado en la recarga: corta el flujo sin motivo")
	}
}

func TestChangedChannelIsRestarted(t *testing.T) {
	m := testManager(t)
	m.apply([]EffCfg{fakeChannel("a")})
	before := m.wk["a"]

	changed := fakeChannel("a")
	changed.TTL = 9
	m.apply([]EffCfg{changed})

	if m.wk["a"] == before {
		t.Fatal("un canal con la config cambiada no se ha reiniciado")
	}
}

// Un panic no puede dejar el canal muerto para siempre: tiene que entrar por el
// mismo camino de reintento que cualquier otro fallo.
func TestPanicInRelayIsRetried(t *testing.T) {
	m := testManager(t)
	m.retryDelay = 10 * time.Millisecond
	var calls int32
	retried := make(chan struct{})
	var once sync.Once
	m.run = func(ctx context.Context, e EffCfg, st *stats) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			panic("boom en el camino de datos")
		}
		once.Do(func() { close(retried) })
		<-ctx.Done()
		return nil
	}

	m.apply([]EffCfg{fakeChannel("a")})

	select {
	case <-retried:
	case <-time.After(3 * time.Second):
		t.Fatal("tras un panic el canal no se reintenta: queda muerto y ninguna recarga lo resucita")
	}
}

// Un canal mudo no puede llenar el log con una línea por reintento.
func TestStalledChannelIsLoggedOnlyOnce(t *testing.T) {
	useLang(t, langEN)
	var buf syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager[EffCfg](ctx, log.New(io.Discard, "", 0), log.New(&buf, "", 0))
	m.retryDelay = 5 * time.Millisecond
	m.run = func(ctx context.Context, e EffCfg, st *stats) error { return errStalled }

	m.apply([]EffCfg{fakeChannel("a")})
	time.Sleep(150 * time.Millisecond) // daría para ~30 reintentos
	m.shutdown()

	if n := strings.Count(buf.String(), "relay down"); n != 1 {
		t.Fatalf("el canal mudo se registró %d veces, quiero 1:\n%s", n, buf.String())
	}
}

// ─── Recarga: no tirar lo que está funcionando ───────────────────────────────

func TestReloadKeepsRunningChannelWhoseNewConfigIsInvalid(t *testing.T) {
	useLang(t, langEN)
	m := testManager(t)
	m.apply([]EffCfg{fakeChannel("a"), fakeChannel("b")})

	// "a" venía en el fichero pero no ha validado; "b" sigue bien.
	desired := m.keepRunning([]EffCfg{fakeChannel("b")}, map[string]bool{"a": true}, relayCompatible)

	if chanNames(desired) != "b,a" {
		t.Fatalf("canales deseados = %q, quiero conservar también \"a\"", chanNames(desired))
	}
}

func TestReloadStopsChannelThatWasRemovedFromTheFile(t *testing.T) {
	m := testManager(t)
	m.apply([]EffCfg{fakeChannel("a"), fakeChannel("b")})

	// "a" ya no aparece en el fichero y nadie lo ha rechazado: eso sí se para.
	desired := m.keepRunning([]EffCfg{fakeChannel("b")}, map[string]bool{}, relayCompatible)

	if chanNames(desired) != "b" {
		t.Fatalf("canales deseados = %q, quiero solo \"b\"", chanNames(desired))
	}
}

// En el relé el choque es un bucle de realimentación: si "b" se conserva con su
// config vieja y "a" ya ha validado apuntando al origen de "b", el flujo da
// vueltas hasta saturar la NIC. Es justo lo que la validación impide al
// arrancar, y conservar a ciegas lo reintroducía.
func TestKeptRelayChannelCannotCloseAFeedbackLoop(t *testing.T) {
	useLang(t, langEN)
	m := testManager(t)

	viejoB := EffCfg{Name: "b", Source: "239.0.20.1:5000",
		Dest: []string{"239.0.10.1:5000"}, TTL: 1} // b: 20.1 -> 10.1
	m.apply([]EffCfg{viejoB})

	// "a" valida y emite hacia el origen de "b": juntos cierran el ciclo.
	nuevoA := EffCfg{Name: "a", Source: "239.0.10.1:5000",
		Dest: []string{"239.0.20.1:5000"}, TTL: 1} // a: 10.1 -> 20.1
	desired := m.keepRunning([]EffCfg{nuevoA}, map[string]bool{"b": true}, relayCompatible)

	if chanNames(desired) != "a" {
		t.Fatalf("canales = %q: se ha conservado uno que cierra un bucle", chanNames(desired))
	}
}

// ─── Estadísticas ────────────────────────────────────────────────────────────

func TestJSONStatsZeroDisablesStats(t *testing.T) {
	zero := 0.0
	c := Config{
		Defaults: Defaults{Stats: &zero},
		Channels: []ChannelCfg{{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"239.255.0.1:1234"}}},
	}

	if r := resolveChannels(c, 10); r.stats != 0 {
		t.Fatalf("intervalo de stats = %v, quiero 0 (el JSON pide desactivarlas)", r.stats)
	}
}

func TestJSONStatsOverridesFlag(t *testing.T) {
	itv := 2.5
	c := Config{
		Defaults: Defaults{Stats: &itv},
		Channels: []ChannelCfg{{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"239.255.0.1:1234"}}},
	}

	if r := resolveChannels(c, 10); r.stats != 2.5 {
		t.Fatalf("intervalo de stats = %v, quiero 2.5", r.stats)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Un SIGHUP que cambia el intervalo debe surtir efecto sin reiniciar el proceso.
func TestStatsIntervalAppliesAfterReload(t *testing.T) {
	var buf syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager[EffCfg](ctx, log.New(&buf, "", 0), log.New(io.Discard, "", 0))
	m.wk["a"] = &worker[EffCfg]{cfg: EffCfg{Name: "a"}, cancel: func() {}, st: &stats{name: "a"}}

	m.setStatsInterval(0) // arranca con las estadísticas apagadas
	go m.statsLoop()
	time.Sleep(150 * time.Millisecond)
	if s := buf.String(); s != "" {
		t.Fatalf("con intervalo 0 no debería imprimir nada, imprimió %q", s)
	}

	m.setStatsInterval(0.05) // recarga: ahora sí
	time.Sleep(400 * time.Millisecond)
	if !strings.Contains(buf.String(), "pkt/s") {
		t.Fatalf("tras cambiar el intervalo no imprimió estadísticas: %q", buf.String())
	}
}

// El resumen tiene que dar las cifras y vaciar los contadores.
func TestReportShowsCountersAndResetsThem(t *testing.T) {
	var buf syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager[EffCfg](ctx, log.New(&buf, "", 0), log.New(io.Discard, "", 0))
	// 1000 bytes entran y 3000 salen: tres destinos.
	st := &stats{name: "a", pkts: 100, byts: 1000, sent: 3000, errs: 4, drops: 8}
	m.wk["a"] = &worker[EffCfg]{cfg: EffCfg{Name: "a"}, cancel: func() {}, st: st}

	m.report(2) // dos segundos

	out := buf.String()
	// pkt va como tasa; err y drop como cuenta del intervalo, porque como tasa
	// un suceso raro se redondearía a cero y no se vería nunca.
	for _, want := range []string{"50 pkt/s", "rx   0.00", "tx   0.01", "4 err", "8 drop"} {
		if !strings.Contains(out, want) {
			t.Errorf("el resumen no contiene %q:\n%s", want, out)
		}
	}
	if st.pkts != 0 || st.byts != 0 || st.sent != 0 || st.errs != 0 || st.drops != 0 {
		t.Errorf("los contadores no se han vaciado: %+v", st)
	}
}

// La salida no puede confundirse con la entrada: con varios destinos, el enlace
// lleva N veces más de lo que entra.
func TestSummaryDistinguishesIngressFromEgress(t *testing.T) {
	var buf syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager[EffCfg](ctx, log.New(&buf, "", 0), log.New(io.Discard, "", 0))
	st := &stats{name: "a", pkts: 1000, byts: 1250000, sent: 3750000} // 10 Mbps in, 30 out
	m.wk["a"] = &worker[EffCfg]{cfg: EffCfg{Name: "a"}, cancel: func() {}, st: st}

	m.report(1)

	out := buf.String()
	if !strings.Contains(out, "rx  10.00") || !strings.Contains(out, "tx  30.00") {
		t.Fatalf("el resumen no separa entrada de salida:\n%s", out)
	}
}

// Un error cada varios segundos no puede desaparecer por redondeo.
func TestSporadicErrorIsVisibleInTheSummary(t *testing.T) {
	var buf syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager[EffCfg](ctx, log.New(&buf, "", 0), log.New(io.Discard, "", 0))
	st := &stats{name: "a", pkts: 10000, errs: 1} // 1 error en 10 s = 0,1/s
	m.wk["a"] = &worker[EffCfg]{cfg: EffCfg{Name: "a"}, cancel: func() {}, st: st}

	m.report(10)

	if strings.Contains(buf.String(), "0 err") {
		t.Fatalf("el error se ha redondeado a cero y es invisible:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "1 err") {
		t.Fatalf("el resumen no da la cuenta de errores:\n%s", buf.String())
	}
}

// Y el motivo del fallo tiene que llegar al log, no solo el contador.
func TestLastSendErrorIsReported(t *testing.T) {
	useLang(t, langEN)
	var errBuf syncBuf
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager[EffCfg](ctx, log.New(io.Discard, "", 0), log.New(&errBuf, "", 0))
	st := &stats{name: "a", errs: 3}
	st.lastErr.Store("write udp 239.255.0.1:1234: network is unreachable")
	m.wk["a"] = &worker[EffCfg]{cfg: EffCfg{Name: "a"}, cancel: func() {}, st: st}

	m.report(1)

	if !strings.Contains(errBuf.String(), "network is unreachable") {
		t.Fatalf("el errno del envío no aparece en el log:\n%s", errBuf.String())
	}

	// Y no se repite si en el intervalo siguiente no ha vuelto a fallar.
	errBuf.b.Reset()
	m.report(1)
	if strings.Contains(errBuf.String(), "network is unreachable") {
		t.Fatalf("el error se repite sin haber vuelto a ocurrir:\n%s", errBuf.String())
	}
}

// ─── Configuración que no puede funcionar ────────────────────────────────────

func TestRejectsAddressWithoutPort(t *testing.T) {
	useLang(t, langEN)
	cases := []struct{ name, src, dst string }{
		{"origen sin puerto", "239.0.10.1:0", "239.255.0.1:1234"},
		{"destino sin puerto", "239.0.10.1:5000", "239.255.0.1:0"},
	}
	for _, c := range cases {
		cfg := Config{Channels: []ChannelCfg{
			{Name: "a", Source: c.src, Dest: []string{c.dst}},
		}}

		r := resolveChannels(cfg, 10)

		if len(r.channels) != 0 {
			t.Errorf("%s: aceptado (%s -> %s); se uniría al grupo y no movería un solo paquete", c.name, c.src, c.dst)
		}
		if !hasWarn(r.warns, "no port") {
			t.Errorf("%s: sin aviso (%v)", c.name, r.warns)
		}
	}
}

func TestClampsAbsurdStatsInterval(t *testing.T) {
	useLang(t, langEN)
	huge := 1e10
	c := Config{
		Defaults: Defaults{Stats: &huge},
		Channels: []ChannelCfg{{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"239.255.0.1:1234"}}},
	}

	r := resolveChannels(c, 10)

	if seconds(r.stats) <= 0 {
		t.Fatalf("intervalo %v: desbordaría time.Duration y statsLoop giraría sin parar", r.stats)
	}
	if !hasWarn(r.warns, "out of range") {
		t.Fatalf("se acota en silencio, sin avisar: %v", r.warns)
	}
}

func TestWarnsAboutUnknownConfigFields(t *testing.T) {
	useLang(t, langEN)
	dir := t.TempDir()
	path := filepath.Join(dir, "typo.json")
	// "defualts" mal escrito: sin aviso, el bloque entero se perdería.
	body := `{"defualts":{"ttl":3},"channels":[{"name":"a","source":"239.0.10.1:5000","dest":["239.255.0.1:1234"]}]}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	c, warns, err := loadConfig(path)

	if err != nil {
		t.Fatalf("un campo desconocido no debe tumbar la config: %v", err)
	}
	if !hasWarn(warns, "unknown field") {
		t.Fatalf("no avisa del campo mal escrito: %v", warns)
	}
	if len(c.Channels) != 1 {
		t.Fatalf("el resto de la config no se ha leído: %+v", c)
	}
}

// Los binarios son estáticos: el ejecutable que se distribuye lleva dentro el
// runtime de Go y las dependencias, y sus licencias BSD piden reproducir el
// aviso al redistribuir en forma binaria. THIRD-PARTY-NOTICES.md es ese aviso.
//
// El fallo clásico de un fichero así es quedarse obsoleto en silencio: alguien
// sube una versión, o añade una dependencia, y el aviso sigue nombrando lo de
// antes. Esto lo convierte en un fallo de compilación de la suite.
func TestThirdPartyNoticesMatchGoMod(t *testing.T) {
	raiz := filepath.Join("..", "..")
	gomod, err := os.ReadFile(filepath.Join(raiz, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	avisos, err := os.ReadFile(filepath.Join(raiz, "THIRD-PARTY-NOTICES.md"))
	if err != nil {
		t.Fatalf("no hay fichero de avisos de terceros: %v", err)
	}
	texto := string(avisos)

	// Las líneas de dependencia son "<ruta> <versión>", dentro o fuera del
	// bloque require. Basta con que la ruta lleve un punto en el primer
	// segmento: eso descarta "module", "go" y las directivas.
	deps := 0
	for _, linea := range strings.Split(string(gomod), "\n") {
		campos := strings.Fields(strings.TrimSpace(linea))
		if len(campos) < 2 || !strings.HasPrefix(campos[1], "v") {
			continue
		}
		ruta, version := campos[0], campos[1]
		if !strings.Contains(strings.SplitN(ruta, "/", 2)[0], ".") {
			continue
		}
		deps++
		if !strings.Contains(texto, ruta) {
			t.Errorf("%s está en go.mod y no aparece en THIRD-PARTY-NOTICES.md: "+
				"hay que añadir su licencia, con el texto literal de su fichero LICENSE", ruta)
			continue
		}
		if !strings.Contains(texto, version) {
			t.Errorf("%s: go.mod dice %s y el fichero de avisos no lo menciona; "+
				"la versión de la tabla se ha quedado atrás", ruta, version)
		}
	}
	if deps == 0 {
		t.Fatal("no se ha reconocido ninguna dependencia en go.mod: el test no está comprobando nada")
	}
	t.Logf("%d dependencias comprobadas contra el fichero de avisos", deps)
}
