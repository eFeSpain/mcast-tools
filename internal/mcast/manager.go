package mcast

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ─── Manager (orquesta canales + recarga) ─────────────────────────────────────

// keyed es lo que el Manager necesita de la configuraciÃ³n de un canal: un
// nombre con el que identificarlo entre recargas y una huella con la que saber
// si ha cambiado. Lo cumplen tanto EffCfg (relÃ©) como SendCfg (emisor), asÃ­ que
// la orquestaciÃ³n es la misma para los dos binarios.
type keyed interface {
	chanName() string
	key() string
	// describe da la línea que se registra al arrancar el canal. Cada
	// herramienta cuenta lo suyo: el relé, origen y destinos; el emisor, a
	// dónde emite y a qué ritmo.
	describe() string
}

type worker[T keyed] struct {
	cfg    T // la config con la que arrancó, para poder conservarlo en una recarga
	key    string
	cancel context.CancelFunc
	st     *stats
	done   chan struct{} // se cierra cuando la goroutine ha soltado los sockets
}

type Manager[T keyed] struct {
	root     context.Context
	info     *log.Logger
	errl     *log.Logger
	mu       sync.Mutex
	wk       map[string]*worker[T]
	statsItv float64       // protegido por mu
	statsChg chan struct{} // avisa a statsLoop de un intervalo nuevo
	// applyMu serializa la convergencia entera. mu protege el mapa durante
	// tramos cortos, pero apply lo suelta a media faena para esperar fuera del
	// lock: sin este segundo cerrojo, dos convergencias solapadas (o un
	// shutdown a mitad de una recarga) podrían pisarse. Hoy main serializa las
	// señales y no ocurre, pero nada lo garantizaba.
	applyMu sync.Mutex
	// stopTimeout limita lo que se espera a un canal que no termina de parar.
	stopTimeout time.Duration
	// retryDelay es la espera entre reintentos de un canal caído.
	retryDelay time.Duration
	// run es el relé de un canal. Es un campo para que los tests puedan
	// sustituirlo y ejercitar el ciclo de vida sin abrir sockets de verdad.
	run func(context.Context, T, *stats) error
	// sender cambia el formato del resumen: el emisor no recibe nada, así que
	// las columnas de entrada y de descartes por grupo no le dicen nada.
	sender      bool
	drained     chan struct{}
	drainedOnce sync.Once
}

func NewManager[T keyed](root context.Context, info, errl *log.Logger) *Manager[T] {
	return &Manager[T]{root: root, info: info, errl: errl, wk: map[string]*worker[T]{},
		statsChg: make(chan struct{}, 1), stopTimeout: 2 * time.Second,
		retryDelay: defRetry, drained: make(chan struct{})}
}

// setStatsInterval cambia el intervalo en caliente (lo usa la recarga por
// SIGHUP). <= 0 apaga las estadísticas sin parar el bucle, para poder
// reactivarlas en otra recarga.
func (m *Manager[T]) setStatsInterval(v float64) {
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

func (m *Manager[T]) statsInterval() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statsItv
}

// runOnce ejecuta un intento y convierte un panic en un error normal. El
// recover tiene que estar AQUÍ dentro y no envolviendo a supervise: si envuelve
// al bucle, un panic lo termina y el canal queda muerto para siempre, con su
// entrada aún en el mapa, invisible en las estadísticas y sin que una recarga
// lo resucite (su config no ha cambiado, así que apply lo da por sano).
func (m *Manager[T]) runOnce(ctx context.Context, e T, st *stats) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf(txt.errPanic, r)
		}
	}()
	return m.run(ctx, e, st)
}

func (m *Manager[T]) supervise(ctx context.Context, e T, st *stats) {
	stalled := false // para no repetir el aviso mientras el canal siga mudo
	for {
		if ctx.Err() != nil {
			return
		}
		err := m.runOnce(ctx, e, st)
		if ctx.Err() != nil {
			return
		}
		// Fuente agotada: el canal ha hecho su trabajo. Tratarlo como una caída
		// lo reabriría cada pocos segundos, y un fichero "sin bucle" acabaría
		// emitiéndose en bucle con un hueco entre pases.
		if errors.Is(err, errSourceDone) {
			m.info.Printf(txt.logChannelDone, e.chanName())
			m.retire(e.chanName())
			return
		}
		if quiet := errors.Is(err, errStalled); !quiet || !stalled {
			m.errl.Printf(txt.logRelayDown, e.chanName(), err, m.retryDelay)
		}
		stalled = errors.Is(err, errStalled)
		select {
		case <-time.After(m.retryDelay):
		case <-ctx.Done():
			return
		}
	}
}

// retire saca del mapa un canal que ha terminado por su cuenta. Si con eso no
// queda ninguno, avisa por drained: un emisor al que se le acaba el material no
// tiene por qué quedarse dando vueltas para siempre.
func (m *Manager[T]) retire(name string) {
	m.mu.Lock()
	delete(m.wk, name)
	vacio := len(m.wk) == 0
	m.mu.Unlock()
	if vacio {
		m.drainedOnce.Do(func() { close(m.drained) })
	}
}

// Drained se cierra cuando todos los canales han terminado su fuente. Sirve
// para que el proceso salga solo en vez de esperar una señal.
func (m *Manager[T]) Drained() <-chan struct{} { return m.drained }

// apply arranca/para/reinicia canales para converger al estado deseado. Entre
// parar un canal y arrancar su relevo espera a que el primero haya soltado los
// sockets: si no, durante esa ventana los dos reenvían y el flujo sale
// duplicado en destino.
func (m *Manager[T]) apply(desired []T) {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	want := map[string]T{}
	for _, e := range desired {
		want[e.chanName()] = e
	}

	// Parar los que sobran o han cambiado.
	m.mu.Lock()
	var stopped []*worker[T]
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
		w := &worker[T]{cfg: e, key: e.key(), cancel: cancel, st: st, done: make(chan struct{})}
		m.wk[name] = w
		m.info.Printf(txt.logStarting, name, e.describe())
		go func() {
			defer close(w.done)
			m.supervise(ctx, e, st)
		}()
	}
}

// keepRunning conserva los canales que siguen emitiendo pero cuya configuración
// nueva no ha validado: se quedan tal y como estaban en vez de pararse. Los que
// simplemente ya no aparecen en el fichero sí se paran, como siempre.
func (m *Manager[T]) keepRunning(desired []T, rejected map[string]bool) []T {
	if len(rejected) == 0 {
		return desired
	}
	have := map[string]bool{}
	for _, e := range desired {
		have[e.chanName()] = true
	}
	names := make([]string, 0, len(rejected))
	for name := range rejected {
		if !have[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, name := range names {
		if w, ok := m.wk[name]; ok {
			desired = append(desired, w.cfg)
			m.errl.Printf(txt.warnKeptRunning, name)
		}
	}
	return desired
}

// waitStopped espera a que los canales ya cancelados cierren sus sockets, con
// un tope común: un relé atascado no debe bloquear la recarga de los demás.
func (m *Manager[T]) waitStopped(ws []*worker[T]) {
	if len(ws) == 0 {
		return
	}
	deadline := time.After(m.stopTimeout)
	for i, w := range ws {
		select {
		case <-w.done:
		case <-deadline:
			// El plazo es común a todos. Al agotarse se nombran TODOS los que
			// siguen pendientes, no solo aquel en el que tocó esperar.
			late := make([]string, 0, len(ws)-i)
			for _, rest := range ws[i:] {
				select {
				case <-rest.done:
				default:
					late = append(late, rest.cfg.chanName())
				}
			}
			m.errl.Printf(txt.logStopTimeout, m.stopTimeout, strings.Join(late, ", "))
			return
		}
	}
}

// statsLoop imprime un resumen cada intervalo. Relee el intervalo en cada
// vuelta, así que una recarga que lo cambie (o lo apague, o lo vuelva a
// encender) surte efecto sin reiniciar el proceso.
func (m *Manager[T]) statsLoop() {
	last := time.Now()
	for {
		// Intervalo <= 0: tick nil, que bloquea hasta que llegue un cambio.
		var tick <-chan time.Time
		var timer *time.Timer
		// seconds acota: un intervalo disparatado no puede desbordar la Duration
		// y dejar el temporizador vencido en cada vuelta.
		if d := seconds(m.statsInterval()); d > 0 {
			timer = time.NewTimer(d)
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

func (m *Manager[T]) report(elapsed float64) {
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
		s := atomic.SwapUint64(&st.sent, 0)
		er := atomic.SwapUint64(&st.errs, 0)
		dr := atomic.SwapUint64(&st.drops, 0)
		// err y drop van como CUENTA del intervalo, no como tasa: son sucesos
		// raros y como tasa se redondearían a cero (un error cada diez segundos
		// son 0,1 err/s, que se imprime "0" y no se ve nunca).
		if m.sender {
			// El emisor no recibe: rx y los descartes por grupo no aplican, y
			// "rebase" son las veces que hubo que rebasar el reloj porque la
			// fuente no daba el bitrate pedido.
			m.info.Printf("[%s] %-16s %7.0f pkt/s · tx %6.2f Mbps · %d err · %d rebase",
				ts, n, float64(p)/elapsed, float64(s)*8/1e6/elapsed, er, dr)
		} else {
			// rx es lo que entra y tx lo que sale de verdad: con N destinos, tx
			// es aproximadamente N veces rx, y es tx lo que ocupa el enlace.
			// drop = datagramas que llegaron al puerto pero no iban dirigidos al
			// grupo de este canal, y por tanto NO se han reenviado.
			m.info.Printf("[%s] %-16s %7.0f pkt/s · rx %6.2f Mbps · tx %6.2f Mbps · %d err · %d drop",
				ts, n, float64(p)/elapsed,
				float64(b)*8/1e6/elapsed, float64(s)*8/1e6/elapsed, er, dr)
		}
		if er > 0 {
			if last, ok := st.lastErr.Swap("").(string); ok && last != "" {
				m.errl.Printf(txt.logLastSendErr, n, last)
			}
		}
		if dr > 0 {
			if last, ok := st.lastDrop.Swap("").(string); ok && last != "" {
				m.errl.Printf(txt.logLastDrop, n, last)
			}
		}
	}
}

func (m *Manager[T]) shutdown() {
	m.applyMu.Lock()
	defer m.applyMu.Unlock()
	m.mu.Lock()
	stopped := make([]*worker[T], 0, len(m.wk))
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

// reopenFile es el destino de -logfile. Puede reabrirse sin parar el proceso,
// que es lo que hace falta para convivir con logrotate: si no, tras la rotación
// el proceso seguiría escribiendo al inodo viejo hasta el siguiente reinicio.
type reopenFile struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

func (r *reopenFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Write(p)
}

func (r *reopenFile) reopen() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	r.mu.Lock()
	old := r.f
	r.f = f
	r.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return nil
}

func (r *reopenFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.f == nil {
		return nil
	}
	return r.f.Close()
}
