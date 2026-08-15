package mcast

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/ipv4"
)

// Prueba del camino de datos completo: mcast-send emite un patrón numerado,
// mcast-dup lo reenvía a otro grupo y aquí se comprueba que llega entero, sin
// duplicados, sin huecos y sin colarse tráfico de otro canal.
//
// Era el agujero de cobertura que quedaba: el reenvío en sí solo se verificaba
// a mano porque generar tráfico multicast requería un emisor. Ahora lo hay.

// multicastIface busca una NIC utilizable. Sin ella el test se salta en vez de
// fallar: en un contenedor sin red no hay nada que probar.
func multicastIface(t *testing.T) *net.Interface {
	t.Helper()
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skip("no se pueden enumerar las interfaces:", err)
	}
	for i := range ifaces {
		ifi := &ifaces[i]
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		addrs, _ := ifi.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && ipn.IP.To4() != nil && !ipn.IP.IsLoopback() {
				return ifi
			}
		}
	}
	t.Skip("sin interfaz IPv4 con multicast: no se puede probar el camino de datos")
	return nil
}

// receiver escucha un grupo y devuelve los números de secuencia recibidos.
func receiver(t *testing.T, group string, port int, ifi *net.Interface) *ipv4.PacketConn {
	t.Helper()
	lc := net.ListenConfig{Control: func(_, _ string, c syscall.RawConn) error {
		return c.Control(setReuse)
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf(":%d", port))
	if err != nil {
		t.Fatalf("bind receptor: %v", err)
	}
	p := ipv4.NewPacketConn(pc)
	if err := p.JoinGroup(ifi, &net.UDPAddr{IP: net.ParseIP(group)}); err != nil {
		p.Close()
		t.Skipf("no se puede unir a %s por %s: %v", group, ifi.Name, err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestEndToEndSendRelayReceive(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: se salta la prueba con tráfico real")
	}
	ifi := multicastIface(t)

	const (
		srcGroup = "239.77.1.1"
		srcPort  = 5077
		dstGroup = "239.77.2.2"
		otroDest = "239.77.3.3" // el destino de un canal que no debe recibir nada
		dstPort  = 5078
		size     = 1316
		want     = 60
	)
	quiet := log.New(io.Discard, "", 0)

	// Receptores: el destino del canal y un grupo ajeno que sirve de control.
	rx := receiver(t, dstGroup, dstPort, ifi)
	control := receiver(t, otroDest, dstPort, ifi)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// El relé: del grupo de origen al de destino.
	relayCfg := EffCfg{
		Name: "e2e", Source: fmt.Sprintf("%s:%d", srcGroup, srcPort),
		Dest: []string{fmt.Sprintf("%s:%d", dstGroup, dstPort)},
		// TTL 0 = no sale de esta máquina; con loopback basta para la prueba.
		Iface: ifi.Name, TTL: 0, Loop: true, Rcvbuf: 1 << 20,
	}
	relayDone := make(chan error, 1)
	go func() { relayDone <- runRelay(ctx, relayCfg, &stats{name: "e2e"}, quiet, quiet) }()
	time.Sleep(300 * time.Millisecond) // que el join cuaje antes de emitir

	// El emisor: patrón numerado a ~2 Mbps.
	sendCfg := SendCfg{
		Name: "e2e", Dest: fmt.Sprintf("%s:%d", srcGroup, srcPort),
		Iface: ifi.Name, TTL: 0, Loopback: true,
		Bitrate: 2_000_000, Size: size,
	}
	sendDone := make(chan error, 1)
	go func() { sendDone <- runSender(ctx, sendCfg, &stats{name: "e2e"}, quiet) }()

	// Recolección.
	seen := map[uint64]int{}
	buf := make([]byte, 2048)
	deadline := time.Now().Add(8 * time.Second)
	for len(seen) < want && time.Now().Before(deadline) {
		rx.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, _, err := rx.ReadFrom(buf)
		if err != nil {
			break
		}
		if n != size {
			t.Errorf("datagrama de %d bytes, esperaba %d: el relé no reenvía íntegro", n, size)
		}
		seen[binary.BigEndian.Uint64(buf[:8])]++
	}
	cancel()

	if len(seen) < want {
		t.Fatalf("recibidos %d datagramas de %d: el camino emisor -> relé -> receptor no funciona", len(seen), want)
	}

	// Ni duplicados...
	for seq, n := range seen {
		if n > 1 {
			t.Errorf("el datagrama %d llegó %d veces", seq, n)
		}
	}
	// ...ni huecos: el patrón es consecutivo, así que entre el mínimo y el
	// máximo recibidos no puede faltar ninguno.
	var min, max uint64 = ^uint64(0), 0
	for seq := range seen {
		if seq < min {
			min = seq
		}
		if seq > max {
			max = seq
		}
	}
	if got := max - min + 1; int(got) != len(seen) {
		t.Errorf("huecos en la secuencia: %d..%d son %d datagramas pero llegaron %d", min, max, got, len(seen))
	}

	// Y el grupo ajeno no puede haber visto nada.
	control.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, _, _, err := control.ReadFrom(buf); err == nil {
		t.Errorf("han llegado %d bytes a un grupo que nadie alimenta: hay mezcla", n)
	}

	// Y los dos lados tienen que haber parado limpiamente al cancelar.
	for name, done := range map[string]chan error{"relé": relayDone, "emisor": sendDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("%s terminó con error: %v", name, err)
			}
		case <-time.After(3 * time.Second):
			t.Errorf("%s no terminó tras cancelar el contexto", name)
		}
	}
}

// runSender con RTP y con PCR no se ejecutaba en ningún test: solo estaban
// probadas las piezas por separado. Un off-by-one de una línea en el bucle
// —escribir la cabecera sobre el payload, o emitir pkt[:n] en vez de
// pkt[:head+n]— dejaba toda la suite en verde.
func TestSenderEmitsRealRTPWithPCRPacing(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: se salta la prueba con tráfico real")
	}
	ifi := multicastIface(t)

	const (
		group   = "239.79.7.7"
		port    = 5081
		size    = 7 * tsPacket // 1316
		bitrate = 6_000_000
	)

	// Material con PCR de 6 Mbps, servido desde un fichero temporal.
	ts := tsWithPCR(4000, 10, bitrate)
	path := filepath.Join(t.TempDir(), "pcr.ts")
	if err := os.WriteFile(path, ts, 0o644); err != nil {
		t.Fatal(err)
	}

	rx := receiver(t, group, port, ifi)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cfg := SendCfg{
		Name: "rtp", Dest: fmt.Sprintf("%s:%d", group, port), Iface: ifi.Name,
		TTL: 0, Loopback: true, Bitrate: bitrate, Size: size,
		File: path, Loop: true, RTP: true, PCR: true,
	}
	go runSender(ctx, cfg, &stats{name: "rtp"}, log.New(io.Discard, "", 0))

	buf := make([]byte, 2048)
	var primera, ultima uint16
	recibidos := 0
	deadline := time.Now().Add(6 * time.Second)
	for recibidos < 20 && time.Now().Before(deadline) {
		rx.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, _, err := rx.ReadFrom(buf)
		if err != nil {
			break
		}
		// El datagrama tiene que ser cabecera + payload íntegro.
		if n != rtpHeaderLen+size {
			t.Fatalf("datagrama de %d bytes, quiero %d (12 de RTP + %d de payload)",
				n, rtpHeaderLen+size, size)
		}
		if buf[0] != 0x80 || buf[1] != rtpPayloadTypeMP2T {
			t.Fatalf("cabecera RTP mal formada: %#x %#x", buf[0], buf[1])
		}
		// Y el payload tiene que empezar donde empieza un paquete TS: si la
		// cabecera se hubiera escrito encima, aquí no habría un 0x47.
		if buf[rtpHeaderLen] != 0x47 {
			t.Fatalf("el payload no empieza en un paquete TS: %#x", buf[rtpHeaderLen])
		}
		seq := uint16(buf[2])<<8 | uint16(buf[3])
		if recibidos == 0 {
			primera = seq
		}
		ultima = seq
		recibidos++
	}
	cancel()

	if recibidos < 20 {
		t.Fatalf("recibidos %d datagramas de 20", recibidos)
	}
	if avance := ultima - primera; int(avance) != recibidos-1 {
		t.Fatalf("la secuencia RTP avanzó %d en %d datagramas: hay huecos o repeticiones",
			avance, recibidos)
	}
}

// El hallazgo grave de la revisión de RTP/PCR, con un parón de verdad.
//
// Cuando la fuente se para y el emisor decide no recuperar el retraso, tiene
// que re-anclar el reloj que MANDA. Si solo re-ancla el de bitrate fijo, el
// objetivo lo sigue calculando el pacer de PCR sobre su ancla vieja, cada
// vuelta del bucle vuelve a verse retrasada, no se duerme nunca y el retraso
// acumulado sale de golpe a velocidad de cable.
//
// Se mide de las dos formas: el ritmo tras el parón y el número de re-anclajes.
// El segundo es el testigo más limpio —con el fallo son miles, uno por vuelta.
func TestSenderDoesNotBurstAfterAStall(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: se salta la prueba con tráfico real")
	}
	ifi := multicastIface(t)

	const (
		group   = "239.79.9.9"
		port    = 5082
		size    = 7 * tsPacket
		bitrate = 6_000_000
		parada  = 1500 * time.Millisecond
	)

	// La fuente es la entrada estándar, que es lo único que permite pararla a
	// voluntad desde el test.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	viejo := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = viejo; r.Close() })

	ts := tsWithPCR(24000, 10, bitrate) // ~4,5 MB, 6 s de material
	corte := 300 << 10                  // 300 kB antes del parón: 0,4 s
	go func() {
		defer w.Close()
		w.Write(ts[:corte])
		time.Sleep(parada)
		w.Write(ts[corte:]) // y a partir de aquí, tan rápido como acepte el pipe
	}()

	rx := receiver(t, group, port, ifi)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st := &stats{name: "stall"}
	cfg := SendCfg{
		Name: "stall", Dest: fmt.Sprintf("%s:%d", group, port), Iface: ifi.Name,
		TTL: 0, Loopback: true, Bitrate: bitrate, Size: size,
		Stdin: true, PCR: true,
	}
	go runSender(ctx, cfg, st, log.New(io.Discard, "", 0))

	// Se anota la llegada de cada datagrama durante toda la prueba. No vale
	// medir el ritmo medio: la ráfaga dura decenas de milisegundos y un
	// promedio de un segundo se la traga. Lo que hace daño es el PICO, que es
	// lo que desborda el búfer del decodificador.
	type llegada struct {
		t time.Time
		n int
	}
	var llegadas []llegada
	buf := make([]byte, 2048)
	inicio := time.Now()
	fin := inicio.Add(3200 * time.Millisecond)
	for time.Now().Before(fin) {
		rx.SetReadDeadline(fin)
		n, _, _, err := rx.ReadFrom(buf)
		if err != nil {
			break
		}
		llegadas = append(llegadas, llegada{time.Now(), n})
	}
	cancel()

	if len(llegadas) == 0 {
		t.Fatal("no ha llegado nada: el emisor se ha quedado mudo")
	}
	// Sin pacing la fuente entera sale de golpe y luego hay silencio; con
	// pacing el flujo sigue vivo al acabar la ventana.
	if callado := fin.Sub(llegadas[len(llegadas)-1].t); callado > 300*time.Millisecond {
		t.Errorf("el último datagrama llegó %v antes del final: la emisión se agotó de golpe", callado)
	}

	const vent = 200 * time.Millisecond
	var pico float64
	for i := range llegadas {
		hasta := llegadas[i].t.Add(vent)
		total := 0
		for j := i; j < len(llegadas) && llegadas[j].t.Before(hasta); j++ {
			total += llegadas[j].n
		}
		if r := float64(total) * 8 / vent.Seconds(); r > pico {
			pico = r
		}
	}
	reanclajes := atomic.LoadUint64(&st.drops)
	t.Logf("%d datagramas; pico en %v: %.1f Mbps sobre %.1f nominales; %d re-anclajes",
		len(llegadas), vent, pico/1e6, float64(bitrate)/1e6, reanclajes)

	if pico > 3*bitrate {
		t.Errorf("pico de %.1f Mbps sobre un nominal de %.1f: el parón ha salido como ráfaga",
			pico/1e6, float64(bitrate)/1e6)
	}
	// Un parón necesita un re-anclaje, no miles: cada vuelta del bucle sin
	// dormir es uno más.
	if reanclajes > 10 {
		t.Errorf("%d re-anclajes para un solo parón: el pacer de PCR no se está re-anclando", reanclajes)
	}
}

// El pacing tiene que respetar el bitrate pedido: si se acumulara la deriva de
// cada sueño, un flujo de una hora acabaría minutos desfasado.
func TestSenderRespectsBitrate(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: se salta la prueba con tráfico real")
	}
	ifi := multicastIface(t)

	const (
		size    = 1316
		bitrate = 4_000_000 // ~380 paquetes/s
		dur     = 1500 * time.Millisecond
	)
	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer cancel()

	st := &stats{name: "rate"}
	cfg := SendCfg{
		Name: "rate", Dest: "239.77.4.4:5079", Iface: ifi.Name,
		TTL: 0, Loopback: false, Bitrate: bitrate, Size: size,
	}
	start := time.Now()
	if err := runSender(ctx, cfg, st, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("emisor: %v", err)
	}
	elapsed := time.Since(start).Seconds()

	got := float64(st.sent) * 8 / elapsed
	ratio := got / float64(bitrate)
	// Margen amplio: en una máquina de CI cargada el temporizador no es
	// preciso. Lo que se comprueba es que no vaya al doble ni a la mitad.
	if ratio < 0.75 || ratio > 1.25 {
		t.Fatalf("emitidos %.2f Mbps con %.2f pedidos (ratio %.2f)",
			got/1e6, float64(bitrate)/1e6, ratio)
	}
}
