// Relé multicast en Go: duplica flujos UDP de un grupo a otro(s).
//
// Dos modos:
//   - Flags (un canal):   mcast-dup -s ORIGEN:PUERTO -d DEST1,DEST2 [opciones]
//   - Daemon (JSON):      mcast-dup -config /etc/mcast-dup.json
//
// En modo daemon levanta una goroutine por canal y recarga la config con SIGHUP
// (arranca los nuevos, para los quitados, reinicia los cambiados) sin cortar el resto.
//
// Los mensajes salen en el idioma del sistema (español si lo está, inglés si
// no); se puede forzar con -lang. Ver i18n.go.
//
// Compilar (binario estático):
//
//	go mod download
//	CGO_ENABLED=0 go build -o mcast-dup .
//	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o mcast-dup .   # cruzado
//
// Nota: SetReadBuffer/SetWriteBuffer los recorta el kernel a net.core.rmem_max /
// wmem_max. Para buffers grandes efectivos, sube esos sysctl en la cabecera.
//
// Nota: varios canales pueden compartir el puerto de origen con grupos
// distintos y cada uno recibe solo el suyo. En Linux eso exige
// IP_MULTICAST_ALL=0 (ver control_linux.go); Windows y los BSD ya se comportan
// así de serie.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"
)

const (
	defTTL    = 8
	defRcvbuf = 4 * 1024 * 1024
	defStats  = 10.0
)

// ─── Config (JSON) ──────────────────────────────────────────────────────────

type Defaults struct {
	Iface  string   `json:"iface"`
	TTL    int      `json:"ttl"`
	Loop   *bool    `json:"loop"`
	Rcvbuf int      `json:"rcvbuf"`
	Sndbuf int      `json:"sndbuf"`
	Stats  *float64 `json:"stats"`
}

type ChannelCfg struct {
	Name   string   `json:"name"`
	Source string   `json:"source"`
	Dest   []string `json:"dest"`
	Iface  string   `json:"iface"`
	TTL    *int     `json:"ttl"`
	Loop   *bool    `json:"loop"`
	Rcvbuf *int     `json:"rcvbuf"`
	Sndbuf *int     `json:"sndbuf"`
}

type Config struct {
	Defaults Defaults     `json:"defaults"`
	Channels []ChannelCfg `json:"channels"`
}

// EffCfg = config efectiva de un canal (defaults + overrides ya resueltos).
type EffCfg struct {
	Name   string
	Source string
	Dest   []string
	Iface  string
	TTL    int
	Loop   bool
	Rcvbuf int
	Sndbuf int
}

func (e EffCfg) key() string { b, _ := json.Marshal(e); return string(b) }

func loadConfig(path string) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf(txt.errBadJSON, err)
	}
	return c, nil
}

// feedback es el grafo dirigido origen→destinos de los canales ya aceptados.
// Sirve para detectar realimentaciones: reenviar a un grupo que, directa o
// indirectamente, vuelve a alimentar el origen del canal. Con loopback
// multicast activado eso multiplica el flujo en cada vuelta hasta saturar la
// NIC, así que el canal que cierra el ciclo se rechaza.
type feedback map[string][]string

// wouldLoop dice si añadir las aristas src→dests cerraría un ciclo, y por qué
// destino. El grafo acumulado se mantiene acíclico, así que basta con mirar si
// algún destino alcanza ya al origen.
func (g feedback) wouldLoop(src string, dests []string) (string, bool) {
	for _, d := range dests {
		if d == src || g.reaches(d, src, map[string]bool{}) {
			return d, true
		}
	}
	return "", false
}

func (g feedback) reaches(from, target string, seen map[string]bool) bool {
	if seen[from] {
		return false
	}
	seen[from] = true
	for _, next := range g[from] {
		if next == target || g.reaches(next, target, seen) {
			return true
		}
	}
	return false
}

func (g feedback) add(src string, dests []string) {
	g[src] = append(g[src], dests...)
}

// configFromFlags convierte el modo flags en una Config de un solo canal, para
// que pase exactamente por las mismas validaciones que el modo daemon.
func configFromFlags(src, dst, iface string, ttl int, loop bool, rcvbuf, sndbuf int) Config {
	var dests []string
	for _, d := range strings.Split(dst, ",") {
		if d = strings.TrimSpace(d); d != "" {
			dests = append(dests, d)
		}
	}
	return Config{Channels: []ChannelCfg{{
		Name:   "cli",
		Source: strings.TrimSpace(src),
		Dest:   dests,
		Iface:  iface,
		TTL:    &ttl,
		Loop:   &loop,
		Rcvbuf: &rcvbuf,
		Sndbuf: &sndbuf,
	}}}
}

// resolveChannels aplica defaults a cada canal y valida. Devuelve también el
// intervalo de stats efectivo.
func resolveChannels(c Config, statsFlag float64) ([]EffCfg, float64, []string) {
	d := c.Defaults
	if d.TTL == 0 {
		d.TTL = defTTL
	}
	if d.Rcvbuf == 0 {
		d.Rcvbuf = defRcvbuf
	}
	dLoop := true
	if d.Loop != nil {
		dLoop = *d.Loop
	}
	// El puntero ya distingue "no configurado" de 0, así que un 0 explícito en
	// el JSON apaga las estadísticas en vez de caer al valor del flag.
	stats := statsFlag
	if d.Stats != nil {
		stats = *d.Stats
	}

	var out []EffCfg
	var warns []string
	seen := map[string]bool{}
	graph := feedback{}
	for i, ch := range c.Channels {
		name := ch.Name
		if name == "" {
			name = fmt.Sprintf("ch%d", i+1)
		}
		if seen[name] {
			warns = append(warns, fmt.Sprintf(txt.warnDupChannel, name))
			continue
		}
		if ch.Source == "" || len(ch.Dest) == 0 {
			warns = append(warns, fmt.Sprintf(txt.warnNoSourceDest, name))
			continue
		}
		sa, err := net.ResolveUDPAddr("udp4", ch.Source)
		if err != nil {
			warns = append(warns, fmt.Sprintf(txt.warnBadSource, name, ch.Source))
			continue
		}
		if !sa.IP.IsMulticast() {
			warns = append(warns, fmt.Sprintf(txt.warnNotMulticast, name, ch.Source))
			continue
		}
		bad := false
		dkeys := make([]string, 0, len(ch.Dest))
		for _, dst := range ch.Dest {
			da, err := net.ResolveUDPAddr("udp4", dst)
			if err != nil {
				warns = append(warns, fmt.Sprintf(txt.warnBadDest, name, dst))
				bad = true
				continue
			}
			dkeys = append(dkeys, da.String())
		}
		if bad {
			continue
		}
		if via, loops := graph.wouldLoop(sa.String(), dkeys); loops {
			warns = append(warns, fmt.Sprintf(txt.warnFeedbackLoop,
				name, via, sa.String()))
			continue
		}
		graph.add(sa.String(), dkeys)
		e := EffCfg{Name: name, Source: ch.Source, Dest: ch.Dest, Iface: d.Iface,
			TTL: d.TTL, Loop: dLoop, Rcvbuf: d.Rcvbuf, Sndbuf: d.Sndbuf}
		if ch.Iface != "" {
			e.Iface = ch.Iface
		}
		if ch.TTL != nil {
			e.TTL = *ch.TTL
		}
		if ch.Loop != nil {
			e.Loop = *ch.Loop
		}
		if ch.Rcvbuf != nil {
			e.Rcvbuf = *ch.Rcvbuf
		}
		if ch.Sndbuf != nil {
			e.Sndbuf = *ch.Sndbuf
		}
		seen[name] = true
		out = append(out, e)
	}
	return out, stats, warns
}

// ─── Relé de un canal ─────────────────────────────────────────────────────────

type stats struct {
	name             string
	pkts, byts, errs uint64
}

func resolveIface(ip string) (*net.Interface, error) {
	if ip == "" {
		return nil, nil
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	for i := range ifaces {
		addrs, _ := ifaces[i].Addrs()
		for _, a := range addrs {
			var aip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				aip = v.IP
			case *net.IPAddr:
				aip = v.IP
			}
			if aip != nil && aip.String() == ip {
				return &ifaces[i], nil
			}
		}
	}
	return nil, fmt.Errorf(txt.errNoIface, ip)
}

func reuseControl(_, _ string, c syscall.RawConn) error {
	return c.Control(setReuse)
}

// runRelay monta los sockets y reenvía hasta que ctx se cancela o hay error grave.
func runRelay(ctx context.Context, e EffCfg, st *stats) error {
	saddr, err := net.ResolveUDPAddr("udp4", e.Source)
	if err != nil {
		return fmt.Errorf(txt.errSource, e.Source, err)
	}
	ifi, err := resolveIface(e.Iface)
	if err != nil {
		return err
	}
	dests := make([]*net.UDPAddr, 0, len(e.Dest))
	for _, d := range e.Dest {
		a, err := net.ResolveUDPAddr("udp4", d)
		if err != nil {
			return fmt.Errorf(txt.errDest, d, err)
		}
		dests = append(dests, a)
	}

	// RX: bind reusable al puerto + join al grupo origen. El bind es siempre al
	// comodín (el paquete net reescribe los binds multicast a 0.0.0.0), así que
	// el filtrado por grupo lo hace setReuse donde la plataforma lo permite.
	lc := net.ListenConfig{Control: reuseControl}
	pc, err := lc.ListenPacket(ctx, "udp4", fmt.Sprintf(":%d", saddr.Port))
	if err != nil {
		return fmt.Errorf(txt.errBindRx, err)
	}
	defer pc.Close()
	rx := ipv4.NewPacketConn(pc)
	if err := rx.JoinGroup(ifi, &net.UDPAddr{IP: saddr.IP}); err != nil {
		return fmt.Errorf(txt.errJoin, e.Source, err)
	}
	if uc, ok := pc.(*net.UDPConn); ok {
		_ = uc.SetReadBuffer(e.Rcvbuf)
	}

	// TX: TTL, interfaz de salida, loopback y (opcional) buffer de envío.
	tpc, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		return fmt.Errorf(txt.errTxSocket, err)
	}
	defer tpc.Close()
	tx := ipv4.NewPacketConn(tpc)
	_ = tx.SetMulticastTTL(e.TTL)
	_ = tx.SetMulticastLoopback(e.Loop)
	if ifi != nil {
		_ = tx.SetMulticastInterface(ifi)
	}
	if e.Sndbuf > 0 {
		if uc, ok := tpc.(*net.UDPConn); ok {
			_ = uc.SetWriteBuffer(e.Sndbuf)
		}
	}

	// Cierra los sockets al cancelar el contexto, para desbloquear ReadFrom.
	go func() {
		<-ctx.Done()
		pc.Close()
		tpc.Close()
	}()

	buf := make([]byte, 65536)
	for {
		n, _, _, err := rx.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil // parada limpia
			}
			return fmt.Errorf(txt.errRecv, err)
		}
		data := buf[:n]
		for _, da := range dests {
			if _, e := tx.WriteTo(data, nil, da); e != nil {
				atomic.AddUint64(&st.errs, 1)
			}
		}
		atomic.AddUint64(&st.pkts, 1)
		atomic.AddUint64(&st.byts, uint64(n))
	}
}

// ─── Manager (orquesta canales + recarga) ─────────────────────────────────────

type worker struct {
	name   string
	key    string
	cancel context.CancelFunc
	st     *stats
	done   chan struct{} // se cierra cuando la goroutine ha soltado los sockets
}

type Manager struct {
	root     context.Context
	info     *log.Logger
	errl     *log.Logger
	mu       sync.Mutex
	wk       map[string]*worker
	statsItv float64       // protegido por mu
	statsChg chan struct{} // avisa a statsLoop de un intervalo nuevo
	// stopTimeout limita lo que se espera a un canal que no termina de parar.
	stopTimeout time.Duration
}

func NewManager(root context.Context, info, errl *log.Logger) *Manager {
	return &Manager{root: root, info: info, errl: errl, wk: map[string]*worker{},
		statsChg: make(chan struct{}, 1), stopTimeout: 2 * time.Second}
}

// setStatsInterval cambia el intervalo en caliente (lo usa la recarga por
// SIGHUP). <= 0 apaga las estadísticas sin parar el bucle, para poder
// reactivarlas en otra recarga.
func (m *Manager) setStatsInterval(v float64) {
	m.mu.Lock()
	changed := m.statsItv != v
	m.statsItv = v
	m.mu.Unlock()
	if !changed {
		return
	}
	select {
	case m.statsChg <- struct{}{}:
	default: // ya hay un aviso pendiente
	}
}

func (m *Manager) statsInterval() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statsItv
}

func (m *Manager) supervise(ctx context.Context, e EffCfg, st *stats) {
	defer func() {
		if r := recover(); r != nil {
			m.errl.Printf(txt.logPanic, e.Name, r)
		}
	}()
	for {
		if ctx.Err() != nil {
			return
		}
		err := runRelay(ctx, e, st)
		if ctx.Err() != nil {
			return
		}
		m.errl.Printf(txt.logRelayDown, e.Name, err)
		select {
		case <-time.After(3 * time.Second):
		case <-ctx.Done():
			return
		}
	}
}

// apply arranca/para/reinicia canales para converger al estado deseado. Entre
// parar un canal y arrancar su relevo espera a que el primero haya soltado los
// sockets: si no, durante esa ventana los dos reenvían y el flujo sale
// duplicado en destino.
func (m *Manager) apply(desired []EffCfg) {
	want := map[string]EffCfg{}
	for _, e := range desired {
		want[e.Name] = e
	}

	// Parar los que sobran o han cambiado.
	m.mu.Lock()
	var stopped []*worker
	for name, w := range m.wk {
		e, ok := want[name]
		reason := txt.logStopped
		switch {
		case ok && e.key() == w.key:
			continue
		case ok:
			reason = txt.logRestarting
		}
		w.cancel()
		delete(m.wk, name)
		stopped = append(stopped, w)
		m.info.Printf("[%s] %s", name, reason)
	}
	m.mu.Unlock()

	// Esperar fuera del lock, para no bloquear las estadísticas mientras tanto.
	m.waitStopped(stopped)

	// Arrancar los nuevos / reiniciados.
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, e := range want {
		if _, ok := m.wk[name]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(m.root)
		st := &stats{name: name}
		w := &worker{name: name, key: e.key(), cancel: cancel, st: st, done: make(chan struct{})}
		m.wk[name] = w
		m.info.Printf(txt.logStarting,
			name, e.Source, strings.Join(e.Dest, ","), orDefault(e.Iface, "auto"), e.TTL)
		go func() {
			defer close(w.done)
			m.supervise(ctx, e, st)
		}()
	}
}

// waitStopped espera a que los canales ya cancelados cierren sus sockets, con
// un tope común: un relé atascado no debe bloquear la recarga de los demás.
func (m *Manager) waitStopped(ws []*worker) {
	if len(ws) == 0 {
		return
	}
	deadline := time.After(m.stopTimeout)
	for _, w := range ws {
		select {
		case <-w.done:
		case <-deadline:
			m.errl.Printf(txt.logStopTimeout,
				w.name, m.stopTimeout)
			return
		}
	}
}

// statsLoop imprime un resumen cada intervalo. Relee el intervalo en cada
// vuelta, así que una recarga que lo cambie (o lo apague, o lo vuelva a
// encender) surte efecto sin reiniciar el proceso.
func (m *Manager) statsLoop() {
	last := time.Now()
	for {
		// Intervalo <= 0: tick nil, que bloquea hasta que llegue un cambio.
		var tick <-chan time.Time
		var timer *time.Timer
		if itv := m.statsInterval(); itv > 0 {
			timer = time.NewTimer(time.Duration(itv * float64(time.Second)))
			tick = timer.C
		}
		select {
		case <-m.root.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-m.statsChg:
			if timer != nil {
				timer.Stop()
			}
		case <-tick:
			// Las tasas se calculan con el tiempo realmente transcurrido, para
			// que un cambio de intervalo no falsee el primer resumen.
			elapsed := time.Since(last).Seconds()
			last = time.Now()
			m.report(elapsed)
		}
	}
}

func (m *Manager) report(elapsed float64) {
	if elapsed <= 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.wk))
	for n := range m.wk {
		names = append(names, n)
	}
	sort.Strings(names)
	ts := time.Now().Format("15:04:05")
	for _, n := range names {
		st := m.wk[n].st
		p := atomic.SwapUint64(&st.pkts, 0)
		b := atomic.SwapUint64(&st.byts, 0)
		er := atomic.SwapUint64(&st.errs, 0)
		m.info.Printf("[%s] %-16s %7.0f pkt/s · %6.2f Mbps · %.0f err/s",
			ts, n, float64(p)/elapsed, float64(b)*8/1e6/elapsed, float64(er)/elapsed)
	}
}

func (m *Manager) shutdown() {
	m.mu.Lock()
	stopped := make([]*worker, 0, len(m.wk))
	for name, w := range m.wk {
		w.cancel()
		delete(m.wk, name)
		stopped = append(stopped, w)
	}
	m.mu.Unlock()
	m.waitStopped(stopped)
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ─── main ─────────────────────────────────────────────────────────────────────

func main() {
	// El idioma se resuelve antes de registrar los flags: sus descripciones y
	// la ayuda ya tienen que estar traducidas cuando flag.Parse imprima -h.
	l, err := pickLang(langFromArgs(os.Args[1:]), osLanguage())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	setLang(l)

	configPath := flag.String("config", "", txt.flagConfig)
	logfile := flag.String("logfile", "", txt.flagLogfile)
	src := flag.String("s", "", txt.flagSrc)
	dst := flag.String("d", "", txt.flagDst)
	ifaceIP := flag.String("iface", "", txt.flagIface)
	ttl := flag.Int("ttl", defTTL, txt.flagTTL)
	loop := flag.Bool("loop", true, txt.flagLoop)
	rcvbuf := flag.Int("rcvbuf", defRcvbuf, txt.flagRcvbuf)
	sndbuf := flag.Int("sndbuf", 0, txt.flagSndbuf)
	statsItv := flag.Float64("stats", defStats, txt.flagStats)
	flag.String("lang", "auto", txt.flagLang) // ya leído de os.Args; aquí solo para -h

	flag.Usage = func() {
		w := flag.CommandLine.Output()
		fmt.Fprintln(w, txt.usageTitle)
		fmt.Fprintln(w, txt.usageFlagsMode)
		fmt.Fprintln(w, txt.usageFlagsCmd)
		fmt.Fprintln(w, txt.usageDaemonMode)
		fmt.Fprintln(w, "  mcast-dup -config /etc/mcast-dup.json")
		fmt.Fprintln(w, txt.usageOptions)
		flag.PrintDefaults()
		fmt.Fprintln(w, txt.usageExamples)
		fmt.Fprintln(w, "  mcast-dup -s 239.0.10.1:5000 -d 239.255.0.1:1234,239.255.1.1:1234 -iface 10.30.0.5 -ttl 8")
		fmt.Fprintln(w, "  mcast-dup -config /etc/mcast-dup.json  ", txt.usageReloadHint)
	}
	flag.Parse()

	// Logging: info->stdout, err->stderr; o ambos a -logfile.
	var infoW, errW = os.Stdout, os.Stderr
	if *logfile != "" {
		f, err := os.OpenFile(*logfile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			fmt.Fprintln(os.Stderr, txt.errOpenLogfile, err)
			os.Exit(2)
		}
		defer f.Close()
		infoW, errW = f, f
	}
	info := log.New(infoW, "", log.LstdFlags)
	errl := log.New(errW, "ERROR ", log.LstdFlags)

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	mgr := NewManager(rootCtx, info, errl)

	load := func() bool {
		cfg, err := loadConfig(*configPath)
		if err != nil {
			errl.Println(txt.prefixConfig, err)
			return false
		}
		chans, st, warns := resolveChannels(cfg, *statsItv)
		for _, w := range warns {
			errl.Println(txt.prefixConfig, w)
		}
		if len(chans) == 0 {
			errl.Println(txt.prefixConfig, txt.logNoValidChannels)
			return false
		}
		mgr.setStatsInterval(st)
		mgr.apply(chans)
		return true
	}

	if *configPath != "" {
		info.Printf(txt.logDaemonMode, *configPath)
		if !load() {
			os.Exit(2)
		}
	} else {
		if *src == "" || *dst == "" {
			flag.Usage()
			os.Exit(2)
		}
		// Mismas validaciones que el modo daemon; aquí cualquier aviso es fatal
		// porque solo hay un canal y no quedaría nada que relevar.
		chans, _, warns := resolveChannels(
			configFromFlags(*src, *dst, *ifaceIP, *ttl, *loop, *rcvbuf, *sndbuf), *statsItv)
		for _, w := range warns {
			errl.Println(w)
		}
		if len(chans) == 0 {
			os.Exit(2)
		}
		// La NIC sí se comprueba al arrancar: en modo flags no tiene sentido
		// reintentar cada 3s contra una IP que no existe.
		if _, err := resolveIface(*ifaceIP); err != nil {
			errl.Println(err)
			os.Exit(2)
		}
		e := chans[0]
		info.Printf(txt.logFlagsMode, e.Source, strings.Join(e.Dest, ","))
		mgr.setStatsInterval(*statsItv)
		mgr.apply(chans)
	}

	go mgr.statsLoop()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	for sig := range sigc {
		if sig == syscall.SIGHUP && *configPath != "" {
			info.Println(txt.logReloading)
			load()
			continue
		}
		// shutdown ya espera a que los canales cierren sus sockets.
		info.Println(txt.logStopping)
		mgr.shutdown()
		rootCancel()
		return
	}
}
