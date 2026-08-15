package mcast

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
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
	go func() { relayDone <- runRelay(ctx, relayCfg, &stats{name: "e2e"}, quiet) }()
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
