// Package mcast reúne lo común a las herramientas multicast: configuración,
// resolución de interfaz, sockets, orquestación de canales, estadísticas y
// mensajes en dos idiomas. Los binarios de cmd/ son envoltorios finos.
package mcast

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	defTTL      = 8
	defRcvbuf   = 4 * 1024 * 1024
	defStats    = 10.0
	defWatchdog = 60.0 // segundos sin recibir nada antes de rehacer el socket
	defRetry    = 3 * time.Second
)

// ─── Config (JSON) ──────────────────────────────────────────────────────────

type Defaults struct {
	Iface    string   `json:"iface"`
	TTL      int      `json:"ttl"`
	Loop     *bool    `json:"loop"`
	Rcvbuf   int      `json:"rcvbuf"`
	Sndbuf   int      `json:"sndbuf"`
	Stats    *float64 `json:"stats"`
	Watchdog *float64 `json:"watchdog"`
}

type ChannelCfg struct {
	Name   string   `json:"name"`
	Source string   `json:"source"`
	Dest   []string `json:"dest"`
	// From limita de qué emisores se acepta el grupo (SSM). Con la lista vacía
	// se acepta cualquiera, que es el comportamiento clásico.
	From     []string `json:"from"`
	Iface    string   `json:"iface"`
	TTL      *int     `json:"ttl"`
	Loop     *bool    `json:"loop"`
	Rcvbuf   *int     `json:"rcvbuf"`
	Sndbuf   *int     `json:"sndbuf"`
	Watchdog *float64 `json:"watchdog"`
}

type Config struct {
	Defaults Defaults     `json:"defaults"`
	Channels []ChannelCfg `json:"channels"`
}

// EffCfg = config efectiva de un canal (defaults + overrides ya resueltos).
type EffCfg struct {
	Name     string
	Source   string
	Dest     []string
	From     []string
	Iface    string
	TTL      int
	Loop     bool
	Rcvbuf   int
	Sndbuf   int
	Watchdog time.Duration
}

func (e EffCfg) key() string { b, _ := json.Marshal(e); return string(b) }

func (e EffCfg) chanName() string { return e.Name }

func (e EffCfg) describe() string {
	return fmt.Sprintf("%s -> %s  (iface=%s ttl=%d)", e.Source, strings.Join(e.Dest, ","),
		orDefault(e.Iface, "auto"), e.TTL)
}

// loadConfig lee el JSON y devuelve además avisos sobre campos que no existen.
// Un "defualts" mal escrito se perdería entero en silencio y arrancarías con
// los valores por defecto sin enterarte, así que se avisa; pero no es fatal,
// porque hay quien mete claves tipo "_comment" a propósito (JSON no tiene
// comentarios) y eso no debe tumbar la configuración.
func loadConfig(path string) (Config, []string, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		return c, nil, err
	}
	var warns []string
	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&Config{}); err != nil && strings.Contains(err.Error(), "unknown field") {
		warns = append(warns, fmt.Sprintf(txt.warnUnknownField, err))
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, warns, fmt.Errorf(txt.errBadJSON, err)
	}
	return c, warns, nil
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

// hostIPs devuelve las IPv4 configuradas en esta máquina. Se usa para detectar
// destinos que, sin ser multicast, vuelven a entrar por nuestro propio socket.
func hostIPs() []net.IP {
	var out []net.IP
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for i := range ifaces {
		addrs, _ := ifaces[i].Addrs()
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip4 := ip.To4(); ip4 != nil {
				out = append(out, ip4)
			}
		}
	}
	return out
}

// feedsBackLocally detecta la realimentación que se cuela por la puerta del
// unicast: el socket de recepción se bindea al comodín, así que recibe todo lo
// que llegue a su puerto, no solo el multicast. Mandar a 127.0.0.1, a 0.0.0.0,
// al broadcast o a una IP de esta misma máquina, en un puerto que es origen de
// algún canal, realimenta igual que apuntar al propio grupo.
func feedsBackLocally(da *net.UDPAddr, srcPorts map[int]bool, local []net.IP) bool {
	if da.IP == nil || da.IP.IsMulticast() || !srcPorts[da.Port] {
		return false
	}
	if da.IP.IsLoopback() || da.IP.IsUnspecified() || da.IP.Equal(net.IPv4bcast) {
		return true
	}
	for _, ip := range local {
		if ip.Equal(da.IP) {
			return true
		}
	}
	return false
}

// seconds convierte segundos a Duration acotando el resultado: un valor
// disparatado en el JSON no debe desbordar int64 y volverse negativo.
func seconds(v float64) time.Duration {
	const max = 24 * 3600.0
	if v <= 0 || v != v { // <= 0 o NaN
		return 0
	}
	if v > max {
		v = max
	}
	return time.Duration(v * float64(time.Second))
}

// configFromFlags convierte el modo flags en una Config de un solo canal, para
// que pase exactamente por las mismas validaciones que el modo daemon.
func configFromFlags(src, dst, from, iface string, ttl int, loop bool, rcvbuf, sndbuf int, watchdog float64) Config {
	split := func(s string) []string {
		var out []string
		for _, v := range strings.Split(s, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	dests := split(dst)
	return Config{Channels: []ChannelCfg{{
		Name:     "cli",
		Source:   strings.TrimSpace(src),
		Dest:     dests,
		From:     split(from),
		Iface:    iface,
		TTL:      &ttl,
		Loop:     &loop,
		Rcvbuf:   &rcvbuf,
		Sndbuf:   &sndbuf,
		Watchdog: &watchdog,
	}}}
}

// resolved es el resultado de validar una configuración entera.
type resolved struct {
	channels []EffCfg
	stats    float64
	warns    []string
	// rejected son los canales que SÍ venían en la config pero no pasaron la
	// validación. Una recarga los necesita para distinguir "lo has quitado" de
	// "lo has escrito mal", y no tirar así un canal que estaba emitiendo.
	rejected map[string]bool
}

// resolveChannels aplica defaults a cada canal y valida.
func resolveChannels(c Config, statsFlag float64) resolved {
	return resolveChannelsWith(c, statsFlag, hostIPs())
}

// resolveChannelsWith recibe las IPs locales en vez de consultarlas, para que
// los tests no dependan de la máquina donde se ejecutan.
func resolveChannelsWith(c Config, statsFlag float64, local []net.IP) resolved {
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

	r := resolved{rejected: map[string]bool{}}

	// Un intervalo absurdo desbordaría time.Duration y convertiría statsLoop en
	// un bucle a máxima velocidad, así que se acota y se dice.
	if clamped := seconds(stats).Seconds(); stats > 0 && clamped != stats {
		r.warns = append(r.warns, fmt.Sprintf(txt.warnStatsRange, stats, clamped))
		stats = clamped
	}
	r.stats = stats
	dWatch := defWatchdog
	if d.Watchdog != nil {
		dWatch = *d.Watchdog
	}

	// Puertos de origen de TODOS los canales, aunque alguno se descarte después:
	// hacen falta completos para detectar una realimentación hacia un canal que
	// aparece más abajo en el fichero.
	srcPorts := map[int]bool{}
	for _, ch := range c.Channels {
		if a, err := net.ResolveUDPAddr("udp4", ch.Source); err == nil && a.IP.IsMulticast() {
			srcPorts[a.Port] = true
		}
	}

	seen := map[string]bool{}
	graph := feedback{}
	for _, ch := range c.Channels {
		// El nombre automático se deriva del origen y no del índice: si
		// dependiera del índice, insertar un canal al principio del fichero
		// renombraría a todos los siguientes y el SIGHUP los reiniciaría todos.
		name := ch.Name
		if name == "" {
			name = strings.TrimSpace(ch.Source)
			if name == "" {
				name = "?"
			}
		}
		reject := func(format string, args ...any) {
			r.warns = append(r.warns, fmt.Sprintf(format, args...))
			r.rejected[name] = true
		}
		if seen[name] {
			reject(txt.warnDupChannel, name)
			continue
		}
		if ch.Source == "" || len(ch.Dest) == 0 {
			reject(txt.warnNoSourceDest, name)
			continue
		}
		sa, err := net.ResolveUDPAddr("udp4", ch.Source)
		if err != nil {
			reject(txt.warnBadSource, name, ch.Source)
			continue
		}
		if !sa.IP.IsMulticast() {
			reject(txt.warnNotMulticast, name, ch.Source)
			continue
		}
		// Sin puerto, el bind se iría a uno efímero: el canal se uniría al grupo
		// y no recibiría un solo datagrama, con aspecto de estar sano.
		if sa.Port == 0 {
			reject(txt.warnPortZero, name, ch.Source)
			continue
		}
		ttl := d.TTL
		if ch.TTL != nil {
			ttl = *ch.TTL
		}
		if ttl < 0 || ttl > 255 {
			reject(txt.warnBadTTL, name, ttl)
			continue
		}
		bad := false
		loopVia := ""
		dkeys := make([]string, 0, len(ch.Dest))
		dseen := map[string]bool{}
		for _, dst := range ch.Dest {
			da, err := net.ResolveUDPAddr("udp4", dst)
			if err != nil || da.IP == nil {
				reject(txt.warnBadDest, name, dst)
				bad = true
				continue
			}
			if da.Port == 0 {
				reject(txt.warnPortZero, name, dst)
				bad = true
				continue
			}
			if feedsBackLocally(da, srcPorts, local) && loopVia == "" {
				loopVia = da.String()
			}
			// Un destino repetido emitiría cada paquete dos veces al mismo
			// sitio: el receptor ve duplicados y el enlace, el doble de bitrate.
			if k := da.String(); dseen[k] {
				r.warns = append(r.warns, fmt.Sprintf(txt.warnDupDest, name, k))
			} else {
				dseen[k] = true
				dkeys = append(dkeys, k)
			}
		}
		if bad {
			continue
		}
		// Los destinos se guardan ya canónicos y ordenados: así reordenarlos en
		// el JSON no cambia la key() y no reinicia un canal que no ha cambiado.
		sort.Strings(dkeys)

		// 'from' son las direcciones unicast de los emisores admitidos.
		from := make([]string, 0, len(ch.From))
		for _, f := range ch.From {
			ip := net.ParseIP(strings.TrimSpace(f))
			if ip == nil || ip.To4() == nil || ip.IsMulticast() || ip.IsUnspecified() {
				reject(txt.warnBadFrom, name, f)
				bad = true
				continue
			}
			from = append(from, ip.To4().String())
		}
		if bad {
			continue
		}
		sort.Strings(from)
		if loopVia != "" {
			reject(txt.warnFeedbackLoop, name, loopVia, sa.String())
			continue
		}
		if via, loops := graph.wouldLoop(sa.String(), dkeys); loops {
			reject(txt.warnFeedbackLoop, name, via, sa.String())
			continue
		}
		graph.add(sa.String(), dkeys)
		e := EffCfg{Name: name, Source: ch.Source, Dest: dkeys, From: from, Iface: d.Iface,
			TTL: ttl, Loop: dLoop, Rcvbuf: d.Rcvbuf, Sndbuf: d.Sndbuf,
			Watchdog: seconds(dWatch)}
		if ch.Iface != "" {
			e.Iface = ch.Iface
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
		if ch.Watchdog != nil {
			e.Watchdog = seconds(*ch.Watchdog)
		}
		seen[name] = true
		r.channels = append(r.channels, e)
	}
	return r
}
