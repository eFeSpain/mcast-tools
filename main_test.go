package main

import (
	"bytes"
	"context"
	"io"
	"log"
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

	got, _, warns := resolveChannels(c, 10)

	if len(got) != 0 {
		t.Fatalf("canal que se realimenta aceptado: %s", chanNames(got))
	}
	if !hasWarn(warns, "feedback loop") {
		t.Fatalf("no avisa del bucle: %v", warns)
	}
}

// El idioma elegido tiene que llegar hasta los avisos de validación, no solo
// hasta la ayuda.
func TestWarningsFollowTheSelectedLanguage(t *testing.T) {
	bad := Config{Channels: []ChannelCfg{
		{Name: "a", Source: "10.30.0.5:5000", Dest: []string{"239.255.0.1:1234"}},
	}}

	useLang(t, langES)
	if _, _, warns := resolveChannels(bad, 10); !hasWarn(warns, "no es una dirección multicast") {
		t.Errorf("en español: %v", warns)
	}

	useLang(t, langEN)
	if _, _, warns := resolveChannels(bad, 10); !hasWarn(warns, "is not a multicast address") {
		t.Errorf("en inglés: %v", warns)
	}
}

func TestRejectsCycleBetweenChannels(t *testing.T) {
	useLang(t, langEN)
	c := Config{Channels: []ChannelCfg{
		{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"239.0.20.1:5000"}},
		{Name: "b", Source: "239.0.20.1:5000", Dest: []string{"239.0.10.1:5000"}},
	}}

	got, _, warns := resolveChannels(c, 10)

	if chanNames(got) != "a" {
		t.Fatalf("canales aceptados = %q, quiero solo \"a\"", chanNames(got))
	}
	if !hasWarn(warns, "feedback loop") {
		t.Fatalf("no avisa del bucle: %v", warns)
	}
}

func TestAllowsCascadeBetweenChannels(t *testing.T) {
	// a alimenta al grupo que lee b: es una cascada legítima, no un bucle.
	c := Config{Channels: []ChannelCfg{
		{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"239.0.20.1:5000"}},
		{Name: "b", Source: "239.0.20.1:5000", Dest: []string{"239.0.30.1:5000"}},
	}}

	got, _, warns := resolveChannels(c, 10)

	if chanNames(got) != "a,b" {
		t.Fatalf("canales aceptados = %q, quiero \"a,b\" (avisos: %v)", chanNames(got), warns)
	}
}

// ─── Validación de direcciones ───────────────────────────────────────────────

func TestRejectsNonMulticastSource(t *testing.T) {
	c := Config{Channels: []ChannelCfg{
		{Name: "uni", Source: "10.30.0.5:5000", Dest: []string{"239.255.0.1:1234"}},
	}}

	got, _, warns := resolveChannels(c, 10)

	if len(got) != 0 {
		t.Fatalf("source unicast aceptado: %s", chanNames(got))
	}
	if !hasWarn(warns, "multicast") {
		t.Fatalf("no avisa de que el source no es multicast: %v", warns)
	}
}

func TestAllowsUnicastDest(t *testing.T) {
	// Reenviar a un receptor unicast es un uso válido.
	c := Config{Channels: []ChannelCfg{
		{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"10.30.0.9:1234"}},
	}}

	got, _, warns := resolveChannels(c, 10)

	if chanNames(got) != "a" {
		t.Fatalf("destino unicast rechazado (avisos: %v)", warns)
	}
}

// ─── Modo flags: mismas validaciones que el modo daemon ──────────────────────

func TestFlagsModeSplitsAndTrimsDestinations(t *testing.T) {
	cfg := configFromFlags("239.0.10.1:5000", " 239.255.0.1:1234 , 239.255.1.1:1234 ", "", 8, true, 4096, 0)

	got, _, warns := resolveChannels(cfg, 10)

	if len(got) != 1 {
		t.Fatalf("canales = %d, quiero 1 (avisos: %v)", len(got), warns)
	}
	if len(got[0].Dest) != 2 || got[0].Dest[0] != "239.255.0.1:1234" || got[0].Dest[1] != "239.255.1.1:1234" {
		t.Fatalf("destinos = %q, quiero [239.255.0.1:1234 239.255.1.1:1234]", got[0].Dest)
	}
}

func TestFlagsModeRejectsInvalidSourceInsteadOfPanicking(t *testing.T) {
	cfg := configFromFlags("basura", "239.255.0.1:1234", "", 8, true, 4096, 0)

	got, _, warns := resolveChannels(cfg, 10)

	if len(got) != 0 {
		t.Fatalf("source inválido aceptado: %s", chanNames(got))
	}
	if len(warns) == 0 {
		t.Fatal("source inválido sin aviso")
	}
}

// El ejemplo que se publica en el repo tiene que seguir siendo válido.
func TestExampleConfigIsValid(t *testing.T) {
	c, err := loadConfig("mcast-dup.example.json")
	if err != nil {
		t.Fatalf("no se puede leer el ejemplo: %v", err)
	}

	chans, itv, warns := resolveChannels(c, defStats)

	if len(warns) != 0 {
		t.Fatalf("el ejemplo produce avisos: %v", warns)
	}
	if len(chans) != 3 {
		t.Fatalf("canales = %d, quiero 3", len(chans))
	}
	if itv != 10 {
		t.Fatalf("intervalo de stats = %v, quiero 10", itv)
	}
	if chans[2].TTL != 2 || chans[2].Iface != "10.31.0.5" {
		t.Fatalf("el tercer canal no aplica sus overrides: %+v", chans[2])
	}
}

// ─── Parada de canales ───────────────────────────────────────────────────────

func testManager(t *testing.T) *Manager {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return NewManager(ctx, log.New(io.Discard, "", 0), log.New(io.Discard, "", 0))
}

// Un canal que apunta a una IP de NIC inexistente falla antes de abrir ningún
// socket: sirve para ejercitar apply sin tocar la red.
func offlineChannel(name string) EffCfg {
	return EffCfg{Name: name, Source: "239.0.10.1:5000", Dest: []string{"239.255.0.1:1234"},
		Iface: "203.0.113.99", TTL: 1}
}

// worker de mentira que tarda en soltar los sockets tras la cancelación.
func slowWorker(delay time.Duration, released *int32) *worker {
	done := make(chan struct{})
	return &worker{
		key: "config-vieja",
		st:  &stats{name: "a"},
		cancel: func() {
			go func() {
				time.Sleep(delay)
				atomic.StoreInt32(released, 1)
				close(done)
			}()
		},
		done: done,
	}
}

// Si apply arranca el relevo antes de que el canal anterior cierre sus sockets,
// durante esa ventana el flujo sale duplicado.
func TestApplyWaitsForTheOldWorkerBeforeRestarting(t *testing.T) {
	m := testManager(t)
	var released int32
	m.wk["a"] = slowWorker(150*time.Millisecond, &released)

	m.apply([]EffCfg{offlineChannel("a")})
	defer m.shutdown()

	if atomic.LoadInt32(&released) == 0 {
		t.Fatal("apply arrancó el relevo antes de que el canal anterior soltara los sockets")
	}
}

func TestApplyGivesUpWaitingForAStuckWorker(t *testing.T) {
	m := testManager(t)
	m.stopTimeout = 50 * time.Millisecond
	// cancel que no hace nada y done que nunca se cierra: worker atascado.
	m.wk["a"] = &worker{key: "config-vieja", st: &stats{name: "a"},
		cancel: func() {}, done: make(chan struct{})}

	finished := make(chan struct{})
	go func() {
		m.apply([]EffCfg{offlineChannel("a")})
		close(finished)
	}()

	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("apply se quedó colgado esperando a un canal atascado")
	}
	m.shutdown()
}

func TestShutdownWaitsForWorkersToStop(t *testing.T) {
	m := testManager(t)
	var released int32
	m.wk["a"] = slowWorker(150*time.Millisecond, &released)

	m.shutdown()

	if atomic.LoadInt32(&released) == 0 {
		t.Fatal("shutdown devolvió antes de que los canales soltaran los sockets")
	}
}

// ─── Estadísticas ────────────────────────────────────────────────────────────

func TestJSONStatsZeroDisablesStats(t *testing.T) {
	zero := 0.0
	c := Config{
		Defaults: Defaults{Stats: &zero},
		Channels: []ChannelCfg{{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"239.255.0.1:1234"}}},
	}

	_, got, _ := resolveChannels(c, 10)

	if got != 0 {
		t.Fatalf("intervalo de stats = %v, quiero 0 (el JSON pide desactivarlas)", got)
	}
}

func TestJSONStatsOverridesFlag(t *testing.T) {
	itv := 2.5
	c := Config{
		Defaults: Defaults{Stats: &itv},
		Channels: []ChannelCfg{{Name: "a", Source: "239.0.10.1:5000", Dest: []string{"239.255.0.1:1234"}}},
	}

	_, got, _ := resolveChannels(c, 10)

	if got != 2.5 {
		t.Fatalf("intervalo de stats = %v, quiero 2.5", got)
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
	m := NewManager(ctx, log.New(&buf, "", 0), log.New(io.Discard, "", 0))
	m.wk["a"] = &worker{cancel: func() {}, st: &stats{name: "a"}}

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

func TestFlagsModeKeepsExplicitOptions(t *testing.T) {
	cfg := configFromFlags("239.0.10.1:5000", "239.255.0.1:1234", "10.30.0.5", 3, false, 1<<20, 1<<19)

	got, _, _ := resolveChannels(cfg, 10)

	if len(got) != 1 {
		t.Fatalf("canales = %d, quiero 1", len(got))
	}
	e := got[0]
	if e.Iface != "10.30.0.5" || e.TTL != 3 || e.Loop != false || e.Rcvbuf != 1<<20 || e.Sndbuf != 1<<19 {
		t.Fatalf("opciones no respetadas: %+v", e)
	}
}
