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

func newPayload(e SendCfg) (payload, error) {
	switch {
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

	src, err := newPayload(e)
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

	buf := make([]byte, e.Size)
	// Segundos que ocupa un byte al bitrate pedido. Se pacea por BYTES y no por
	// paquetes: un datagrama corto (la cola de un fichero, un vaciado parcial
	// de la tubería) no puede consumir un periodo entero, o el bitrate real se
	// quedaría por debajo del pedido sin que nadie lo notara.
	perByte := 8 * float64(time.Second) / float64(e.Bitrate)
	start := time.Now()
	var bytesSent float64

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
		if w, err := tx.WriteTo(buf[:n], nil, dst); err != nil {
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

		// Reloj absoluto: cuándo DEBERÍA haber salido este volumen contando
		// desde el arranque. Así el error de cada sueño no se acumula.
		target := start.Add(time.Duration(bytesSent * perByte))
		wait := time.Until(target)
		switch {
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
			atomic.AddUint64(&st.drops, 1) // se cuenta como rebase del reloj
			st.lastDrop.Store(fmt.Sprintf(txt.logClockRebase, (-wait).Round(time.Millisecond)))
			start = time.Now()
			bytesSent = 0
		}
	}
}
