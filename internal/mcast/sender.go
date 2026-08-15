package mcast

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/ipv4"
)

// ─── Emisor de un canal ──────────────────────────────────────────────────────

// El relé va paceado por su entrada: reenvía cuando llega algo y el reloj lo
// pone el emisor original. Un emisor tiene que poner su propio reloj, y ahí
// está toda la dificultad:
//
//   - Un TS a 10 Mbps con payload de 1316 bytes son ~950 paquetes/s: uno cada
//     1,05 ms. Dormir por paquete no funciona en ninguna plataforma, porque la
//     granularidad del temporizador es del mismo orden que el intervalo.
//   - Y no se puede acumular el error: dormir "un intervalo" en cada vuelta
//     suma la deriva de cada sueño, y en una hora vas minutos desfasado.
//
// La solución es un reloj absoluto y ráfagas pequeñas: se calcula cuándo
// DEBERÍA salir el paquete N desde el instante de arranque, se envían los que
// ya tocan y se duerme hasta el siguiente. El error de cada sueño se corrige
// solo en la vuelta siguiente, porque el objetivo no es relativo.

// maxBehind es cuánto se tolera ir por detrás del reloj antes de rebasarlo en
// vez de intentar recuperar el retraso de golpe. Unos pocos periodos típicos.
const maxBehind = 50 * time.Millisecond

// maxAhead es lo más que se espera a que llegue el instante de un datagrama.
// Protege del caso simétrico al de maxBehind: un PCR que salta hacia delante
// dejaría el canal callado justo ese tiempo.
const maxAhead = 2 * time.Second

// errSourceDone dice que la fuente se ha agotado y el canal ha terminado su
// trabajo. NO es una caída: si supervise lo tomara por tal, reabriría el
// fichero cada 3 s y un "-loop-file=false" acabaría emitiendo en bucle igual.
var errSourceDone = errors.New("source done")

// payload produce los datos a emitir. Cada fuente (fichero en bucle, stdin,
// patrón generado) implementa esto y el pacing es el mismo para las tres.
type payload interface {
	// next rellena buf y devuelve cuántos bytes ha puesto. io.EOF termina.
	next(buf []byte) (int, error)
	Close() error
}

// fileLoop emite un fichero una y otra vez. Es el caso de playout y el de
// pruebas con material real.
type fileLoop struct {
	path string
	f    *os.File
	loop bool
}

func openFileLoop(path string, loop bool) (*fileLoop, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &fileLoop{path: path, f: f, loop: loop}, nil
}

func (s *fileLoop) next(buf []byte) (int, error) {
	n, err := io.ReadFull(s.f, buf)
	switch {
	case err == nil:
		return n, nil
	case err == io.ErrUnexpectedEOF:
		// Cola del fichero: se emite lo que hay y se vuelve al principio.
		if s.loop {
			if _, e := s.f.Seek(0, io.SeekStart); e != nil {
				return n, e
			}
		}
		return n, nil
	case err == io.EOF:
		if !s.loop {
			return 0, io.EOF
		}
		if _, e := s.f.Seek(0, io.SeekStart); e != nil {
			return 0, e
		}
		return io.ReadFull(s.f, buf)
	default:
		return n, err
	}
}

func (s *fileLoop) Close() error { return s.f.Close() }

// stdinSource emite lo que le llegue por la entrada estándar, para encadenar
// con ffmpeg y compañía.
type stdinSource struct{ r *bufio.Reader }

func newStdinSource() *stdinSource {
	return &stdinSource{r: bufio.NewReaderSize(os.Stdin, 1<<20)}
}

func (s *stdinSource) next(buf []byte) (int, error) {
	n, err := io.ReadFull(s.r, buf)
	if err == io.ErrUnexpectedEOF {
		return n, nil
	}
	return n, err
}

func (s *stdinSource) Close() error { return nil }

// patternSource genera datagramas numerados. Es lo que permite comprobar en el
// otro extremo que no falta ninguno, que no se repiten y que llegan en orden,
// sin depender de tener un fichero a mano.
type patternSource struct{ seq uint64 }

func (s *patternSource) next(buf []byte) (int, error) {
	binary.BigEndian.PutUint64(buf[:8], s.seq)
	s.seq++
	for i := 8; i < len(buf); i++ {
		buf[i] = byte(i)
	}
	return len(buf), nil
}

func (s *patternSource) Close() error { return nil }

// ─── Fuente por orden externa ────────────────────────────────────────────────

// splitCommand parte una línea de órdenes en argumentos respetando comillas.
// No se pasa por un intérprete a propósito: nada de sh -c. Así no hay
// expansión ni inyección desde un fichero de configuración, y funciona igual en
// Windows, donde no hay sh.
func splitCommand(line string) []string {
	var args []string
	var cur strings.Builder
	var quote rune
	escrito := false
	for _, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			escrito = true
		case r == ' ' || r == '\t':
			if cur.Len() > 0 || escrito {
				args = append(args, cur.String())
				cur.Reset()
				escrito = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 || escrito {
		args = append(args, cur.String())
	}
	return args
}

// tail guarda las últimas líneas de la salida de error de la orden. Sin esto,
// un ffmpeg que muere lo hace sin decir por qué: se ve el canal caer y
// reintentarse cada 3 s sin una sola pista de qué le pasa.
type tail struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func (t *tail) add(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, s)
	if len(t.lines) > t.max {
		t.lines = t.lines[len(t.lines)-t.max:]
	}
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, " | ")
}

// execSource emite lo que escriba en su salida estándar una orden externa,
// normalmente ffmpeg. La orden vive y muere con el canal: si se cae, el canal
// se cae con ella y supervise la vuelve a levantar; si se para el canal, se
// mata la orden.
//
// Es lo que hace que cualquier formato sea una entrada de primera clase sin
// meter códecs aquí dentro, y sin el límite de un solo stdin por proceso.
type execSource struct {
	cmd    *exec.Cmd
	out    io.ReadCloser
	r      *bufio.Reader
	stderr *tail
	waited bool
}

func startExec(ctx context.Context, line string) (*execSource, error) {
	args := splitCommand(line)
	if len(args) == 0 {
		return nil, errors.New(txt.errExecEmpty)
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	// Si el contexto se cancela, no se puede esperar indefinidamente a que la
	// orden cierre sus tuberías: se le da un margen y luego se la mata.
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	s := &execSource{cmd: cmd, out: out, r: bufio.NewReaderSize(out, 1<<20),
		stderr: &tail{max: 5}}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	// La salida de error no se vuelca al log —ffmpeg escribe ahí su progreso y
	// lo inundaría—, pero se guardan las últimas líneas para poder explicarlo
	// si la orden muere.
	go func() {
		sc := bufio.NewScanner(errPipe)
		for sc.Scan() {
			if t := strings.TrimSpace(sc.Text()); t != "" {
				s.stderr.add(t)
			}
		}
	}()
	return s, nil
}

func (s *execSource) next(buf []byte) (int, error) {
	n, err := io.ReadFull(s.r, buf)
	switch err {
	case nil:
		return n, nil
	case io.ErrUnexpectedEOF:
		return n, nil // último trozo antes de que la orden termine
	case io.EOF:
		return 0, s.finish()
	default:
		return n, err
	}
}

// finish traduce el final de la orden: salir con código 0 es haber terminado el
// material, y cualquier otra cosa es una caída que hay que reintentar y, sobre
// todo, explicar.
func (s *execSource) finish() error {
	err := s.wait()
	if err == nil {
		return io.EOF
	}
	return fmt.Errorf(txt.errExecFailed, err, s.stderr.String())
}

func (s *execSource) wait() error {
	if s.waited {
		return nil
	}
	s.waited = true
	return s.cmd.Wait()
}

func (s *execSource) Close() error {
	s.out.Close()
	s.wait()
	return nil
}

func newPayload(ctx context.Context, e SendCfg) (payload, error) {
	switch {
	case e.Exec != "":
		return startExec(ctx, e.Exec)
	case e.File != "":
		return openFileLoop(e.File, e.Loop)
	case e.Stdin:
		return newStdinSource(), nil
	default:
		return &patternSource{}, nil
	}
}

// runSender emite un canal hasta que ctx se cancela o la fuente se agota.
func runSender(ctx context.Context, e SendCfg, st *stats, errl *log.Logger) error {
	dst, err := net.ResolveUDPAddr("udp4", e.Dest)
	if err != nil {
		return fmt.Errorf(txt.errDest, e.Dest, err)
	}
	ifi, err := resolveIface(e.Iface)
	if err != nil {
		return err
	}

	src, err := newPayload(ctx, e)
	if err != nil {
		return fmt.Errorf(txt.errPayload, err)
	}
	defer src.Close()

	pc, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		return fmt.Errorf(txt.errTxSocket, err)
	}
	defer pc.Close()
	tx := ipv4.NewPacketConn(pc)
	if err := tx.SetMulticastTTL(e.TTL); err != nil {
		return fmt.Errorf(txt.errSockopt, "IP_MULTICAST_TTL", err)
	}
	if err := tx.SetMulticastLoopback(e.Loopback); err != nil {
		return fmt.Errorf(txt.errSockopt, "IP_MULTICAST_LOOP", err)
	}
	if ifi != nil {
		if err := tx.SetMulticastInterface(ifi); err != nil {
			return fmt.Errorf(txt.errSockopt, "IP_MULTICAST_IF", err)
		}
	}
	if e.Sndbuf > 0 {
		if uc, ok := pc.(*net.UDPConn); ok {
			if err := uc.SetWriteBuffer(e.Sndbuf); err != nil {
				errl.Printf(txt.warnSockbuf, e.Name, "SO_SNDBUF", e.Sndbuf, err)
			}
		}
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			pc.Close()
		case <-stop:
		}
	}()

	// Red de seguridad: con bitrate 0 o negativo el reloj degenera y el bucle
	// emitiría a velocidad de cable. La config ya lo valida, pero runSender
	// también se llama desde los tests.
	if e.Bitrate < 1 || e.Size < 1 {
		return fmt.Errorf(txt.errBadRate, e.Bitrate, e.Size)
	}

	// Con RTP el datagrama lleva 12 bytes de cabecera delante del payload, así
	// que se reserva sitio y la fuente escribe a partir de ahí.
	head := 0
	var rtp *rtpWriter
	if e.RTP {
		head = rtpHeaderLen
		rtp = newRTPWriter()
	}
	pkt := make([]byte, head+e.Size)
	buf := pkt[head:]

	// Segundos que ocupa un byte al bitrate pedido. Se pacea por BYTES y no por
	// paquetes: un datagrama corto (la cola de un fichero, un vaciado parcial
	// de la tubería) no puede consumir un periodo entero, o el bitrate real se
	// quedaría por debajo del pedido sin que nadie lo notara.
	perByte := 8 * float64(time.Second) / float64(e.Bitrate)
	var pacer *pcrPacer
	if e.PCR {
		pacer = newPCRPacer(float64(e.Bitrate) / 8)
	}
	start := time.Now()
	var bytesSent, startBytes float64

	// rebase mueve el ancla al momento actual en vez de intentar recuperar un
	// desfase de golpe. Tiene que tocar el reloj que MANDA: si hay pacer de PCR
	// activo, reanclar solo el de bitrate fijo no sirve de nada, porque el
	// objetivo lo sigue calculando el pacer sobre su ancla vieja y la ráfaga
	// ocurre igual.
	rebase := func(now time.Time, desfase time.Duration) {
		atomic.AddUint64(&st.drops, 1) // se cuenta como rebase del reloj
		if desfase < 0 {
			desfase = -desfase
		}
		st.lastDrop.Store(fmt.Sprintf(txt.logClockRebase, desfase.Round(time.Millisecond)))
		start, startBytes = now, bytesSent
		if pacer != nil {
			pacer.rebase(now, bytesSent)
		}
	}

	for {
		if ctx.Err() != nil {
			return nil
		}
		n, err := src.next(buf)
		if err == io.EOF {
			return errSourceDone // fuente agotada y sin bucle: fin normal
		}
		if err != nil {
			return fmt.Errorf(txt.errPayload, err)
		}
		if n == 0 {
			continue
		}
		now := time.Now()
		// El PCR se mira ANTES de emitir: es lo que decide cuándo sale.
		if pacer != nil && !pacer.gaveUp {
			pacer.observe(buf[:n], bytesSent, now)
			if !pacer.started && bytesSent > pcrProbeBytes {
				pacer.gaveUp = true
				errl.Printf(txt.warnNoPCR, e.Name, float64(e.Bitrate)/1e6)
			}
		}
		if rtp != nil {
			rtp.header(pkt, now)
		}
		if w, err := tx.WriteTo(pkt[:head+n], nil, dst); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			atomic.AddUint64(&st.errs, 1)
			st.lastErr.Store(err.Error())
		} else {
			atomic.AddUint64(&st.sent, uint64(w))
			atomic.AddUint64(&st.pkts, 1)
		}
		bytesSent += float64(n)

		// Reloj absoluto: cuándo DEBERÍA haber salido este volumen. Con PCR lo
		// dice el propio flujo; si no, el bitrate configurado. En los dos casos
		// es una referencia absoluta, así que el error de cada sueño no se
		// acumula.
		//
		// bytesSent es un eje monótono que NO se reinicia nunca: lo comparten el
		// reloj de bitrate fijo (contra startBytes) y el pacer de PCR (contra su
		// lastBytes). Reiniciarlo dejaba a los dos en sistemas de coordenadas
		// distintos y era justo lo que rompía el freno de más abajo.
		target := start.Add(time.Duration((bytesSent - startBytes) * perByte))
		if pacer != nil && pacer.started {
			if t := pacer.target(bytesSent); !t.IsZero() {
				target = t
			}
		}
		wait := time.Until(target)
		switch {
		case wait > maxAhead:
			// Objetivo absurdamente lejano: un PCR que ha saltado hacia delante
			// (material empalmado, un ffmpeg que rearranca con otro reloj)
			// dejaría el canal mudo todo ese rato. Se rebasa y se sigue.
			rebase(now, wait)
		case wait > 0:
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil
			}
		case -wait > maxBehind:
			// Vamos tan por detrás que recuperar el retraso significaría emitir
			// una ráfaga a velocidad de cable. Pasa cuando la fuente se para
			// (ffmpeg tardando en arrancar, un disco con un hipo) o cuando da
			// menos bitrate del pedido: sin este tope, a partir de ahí el
			// emisor no volvería a dormir nunca y el pacing desaparecería.
			rebase(now, wait)
		}
	}
}
