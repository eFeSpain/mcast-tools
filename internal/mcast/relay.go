package mcast

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"
)

// ─── Relé de un canal ─────────────────────────────────────────────────────────

type stats struct {
	name string
	// byts es lo que entra y sent lo que sale: con varios destinos no son lo
	// mismo, y confundirlos hace creer que ocupas un tercio del enlace real.
	// drops = datagramas descartados por no ir dirigidos al grupo del canal.
	pkts, byts, sent, errs, drops uint64
	// lastErr guarda el texto del último fallo de envío del intervalo: sin él,
	// un destino inalcanzable solo se ve como un contador subiendo, y nunca se
	// sabe si es "network is unreachable" o cualquier otra cosa.
	lastErr atomic.Value // string
	// lastDrop, igual, para los descartes: sin esto no sabrías si te está
	// entrando tráfico de un emisor ajeno o dirigido a otro grupo.
	lastDrop atomic.Value // string
	// ts analiza el transport stream que pasa. Lo crea el gestor antes de
	// lanzar la goroutine del canal, así que el puntero no se escribe nunca
	// desde dos sitios; la concurrencia entre alimentarlo y consultarlo la
	// resuelve él por dentro. Es nil en el emisor, que no recibe nada.
	ts *tsAnalyzer
}

// resolveIface acepta una IP local ("10.30.0.5") o el nombre de la interfaz
// ("eth0", "enp3s0"). El nombre es más preciso: identificar la NIC por IP se
// vuelve ambiguo cuando esa NIC tiene varias IPv4.
//
// Además exige que la interfaz esté operativa y admita multicast: si no, el
// join fallaría o —peor— se haría contra una NIC caída y el canal se quedaría
// mudo sin decir por qué.
func resolveIface(spec string) (*net.Interface, error) {
	if spec == "" {
		return nil, nil
	}
	if net.ParseIP(spec) == nil {
		ifi, err := net.InterfaceByName(spec)
		if err != nil {
			return nil, fmt.Errorf(txt.errNoIface, spec)
		}
		return ifi, checkIface(ifi)
	}
	ip := spec
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
				return &ifaces[i], checkIface(&ifaces[i])
			}
		}
	}
	return nil, fmt.Errorf(txt.errNoIface, ip)
}

// checkIface rechaza una interfaz caída o sin multicast: unirse al grupo por
// ahí deja el canal mudo sin explicar por qué.
func checkIface(ifi *net.Interface) error {
	if ifi.Flags&net.FlagUp == 0 {
		return fmt.Errorf(txt.errIfaceDown, ifi.Name)
	}
	if ifi.Flags&net.FlagMulticast == 0 {
		return fmt.Errorf(txt.errIfaceNoMulticast, ifi.Name)
	}
	return nil
}

func reuseControl(_, _ string, c syscall.RawConn) error {
	return c.Control(setReuse)
}

// errStalled señala que el canal lleva demasiado tiempo sin recibir nada. Es un
// error como otro cualquiera para supervise (rehace el socket), pero se
// distingue para no repetir el aviso en cada reintento de un canal parado.
var errStalled = errors.New("stalled")

// stalled lleva el plazo agotado y se identifica con errStalled, para que el
// mensaje salga traducido y sin prefijos internos.
type stalled struct{ after time.Duration }

func (s stalled) Error() string        { return fmt.Sprintf(txt.errStalled, s.after) }
func (s stalled) Is(target error) bool { return target == errStalled }

// allowedSender comprueba el remitente contra la lista de 'from'. Compara IPs y
// no cadenas: la misma dirección puede venir en formato distinto.
func allowedSender(from []string, src net.Addr) bool {
	ua, ok := src.(*net.UDPAddr)
	if !ok || ua.IP == nil {
		return false
	}
	for _, f := range from {
		if ip := net.ParseIP(f); ip != nil && ip.Equal(ua.IP) {
			return true
		}
	}
	return false
}

// joinGroup se une al grupo. Con 'from' configurado intenta primero un join por
// fuente (SSM, RFC 4607): el filtrado lo hace entonces el kernel y, con IGMPv3
// en la red, el switch ni siquiera nos manda el tráfico de otros emisores.
//
// Si la plataforma no lo implementa —Windows, hoy— cae a un join normal y
// avisa: el filtrado seguirá haciéndose aquí, en el bucle de recepción, que
// protege el flujo igual aunque no ahorre ancho de banda.
func joinGroup(rx *ipv4.PacketConn, ifi *net.Interface, group net.IP, e EffCfg, info, errl *log.Logger) error {
	g := &net.UDPAddr{IP: group}
	if len(e.From) > 0 {
		joined := make([]*net.UDPAddr, 0, len(e.From))
		ok := true
		for _, src := range e.From {
			s := &net.UDPAddr{IP: net.ParseIP(src)}
			if err := rx.JoinSourceSpecificGroup(ifi, g, s); err != nil {
				errl.Printf(txt.warnNoSSM, e.Name, err)
				ok = false
				break
			}
			joined = append(joined, s)
		}
		if ok {
			info.Printf(txt.logSSMJoined, e.Name, e.Source, strings.Join(e.From, ", "))
			return nil
		}
		// Deshace los que sí entraron: mezclar (S,G) y (*,G) en el mismo socket
		// deja un estado que depende de la plataforma.
		for _, s := range joined {
			_ = rx.LeaveSourceSpecificGroup(ifi, g, s)
		}
	}
	if err := rx.JoinGroup(ifi, g); err != nil {
		return fmt.Errorf(txt.errJoin, e.Source, err)
	}
	return nil
}

// runRelay monta los sockets y reenvía hasta que ctx se cancela o hay error grave.
func runRelay(ctx context.Context, e EffCfg, st *stats, info, errl *log.Logger) error {
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
	if err := joinGroup(rx, ifi, saddr.IP, e, info, errl); err != nil {
		return err
	}
	if uc, ok := pc.(*net.UDPConn); ok {
		if err := uc.SetReadBuffer(e.Rcvbuf); err != nil {
			errl.Printf(txt.warnSockbuf, e.Name, "SO_RCVBUF", e.Rcvbuf, err)
		}
	}

	// El bind es al comodín (el paquete net reescribe los binds multicast a
	// 0.0.0.0), así que este socket recibe TODO lo que llegue a su puerto,
	// incluido el unicast dirigido a la máquina y el broadcast. Sin mirar la
	// dirección de destino reenviaríamos tráfico ajeno dentro del grupo, así que
	// pedimos el destino de cada datagrama y filtramos.
	filterDst := rx.SetControlMessage(ipv4.FlagDst, true) == nil
	if !filterDst {
		errl.Printf(txt.warnNoDstFilter, e.Name, saddr.Port)
	}

	// TX: TTL, interfaz de salida, loopback y (opcional) buffer de envío.
	tpc, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		return fmt.Errorf(txt.errTxSocket, err)
	}
	defer tpc.Close()
	tx := ipv4.NewPacketConn(tpc)
	// Estos tres sí son fatales: un TTL que el kernel rechaza deja el flujo sin
	// salir de la subred mientras el log de arranque presume del valor pedido.
	if err := tx.SetMulticastTTL(e.TTL); err != nil {
		return fmt.Errorf(txt.errSockopt, "IP_MULTICAST_TTL", err)
	}
	if err := tx.SetMulticastLoopback(e.Loop); err != nil {
		return fmt.Errorf(txt.errSockopt, "IP_MULTICAST_LOOP", err)
	}
	if ifi != nil {
		if err := tx.SetMulticastInterface(ifi); err != nil {
			return fmt.Errorf(txt.errSockopt, "IP_MULTICAST_IF", err)
		}
	}
	if e.Sndbuf > 0 {
		if uc, ok := tpc.(*net.UDPConn); ok {
			if err := uc.SetWriteBuffer(e.Sndbuf); err != nil {
				errl.Printf(txt.warnSockbuf, e.Name, "SO_SNDBUF", e.Sndbuf, err)
			}
		}
	}

	// Cierra los sockets al cancelar el contexto, para desbloquear ReadFrom. El
	// canal stop la termina cuando runRelay sale por su cuenta: si no, cada
	// reintento dejaría una goroutine viva esperando para siempre.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			pc.Close()
			tpc.Close()
		case <-stop:
		}
	}()

	// Se resuelve una vez, fuera del bucle: el puntero lo pone el gestor antes
	// de arrancar la goroutine, así que aquí ya no cambia.
	analiza := e.Analyze && st.ts != nil

	buf := make([]byte, 65536)
	for {
		if e.Watchdog > 0 {
			// Si la NIC se recrea o cambia de IP, la pertenencia al grupo queda
			// huérfana y ReadFrom se quedaría bloqueado para siempre, en
			// silencio. El plazo convierte ese cuelgue en un error reintentable.
			_ = rx.SetReadDeadline(time.Now().Add(e.Watchdog))
		}
		n, cm, src, err := rx.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil // parada limpia
			}
			var ne net.Error
			if e.Watchdog > 0 && errors.As(err, &ne) && ne.Timeout() {
				return stalled{e.Watchdog}
			}
			return fmt.Errorf(txt.errRecv, err)
		}
		// Descarta lo que no venga dirigido al grupo del canal.
		if filterDst && (cm == nil || !cm.Dst.Equal(saddr.IP)) {
			atomic.AddUint64(&st.drops, 1)
			if cm != nil {
				st.lastDrop.Store(fmt.Sprintf("-> %s (no es el grupo del canal)", cm.Dst))
			}
			continue
		}
		// Y lo que venga de un emisor que no está en 'from'. Se comprueba
		// SIEMPRE, aunque el join SSM haya funcionado: si la red no habla
		// IGMPv3 el kernel puede entregar tráfico de otros emisores igualmente.
		if len(e.From) > 0 && !allowedSender(e.From, src) {
			atomic.AddUint64(&st.drops, 1)
			st.lastDrop.Store(fmt.Sprintf("<- %s (emisor no admitido)", src))
			continue
		}
		data := buf[:n]
		for _, da := range dests {
			w, err := tx.WriteTo(data, nil, da)
			if err != nil {
				atomic.AddUint64(&st.errs, 1)
				st.lastErr.Store(err.Error())
				continue
			}
			atomic.AddUint64(&st.sent, uint64(w))
		}
		atomic.AddUint64(&st.pkts, 1)
		atomic.AddUint64(&st.byts, uint64(n))
		// El análisis va DESPUÉS de reenviar, deliberadamente: el trabajo de
		// este proceso es que el flujo salga, y mirarlo no puede retrasarlo.
		if analiza {
			st.ts.feed(data)
		}
	}
}
