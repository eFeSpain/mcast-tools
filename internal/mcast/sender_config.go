package mcast

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
)

// ─── Config del emisor ───────────────────────────────────────────────────────

const (
	defSize    = 1316 // 7 paquetes TS de 188 bytes: el tamaño clásico de IPTV
	defBitrate = "10M"
)

type SendDefaults struct {
	Iface    string   `json:"iface"`
	TTL      int      `json:"ttl"`
	Loopback *bool    `json:"loopback"`
	Sndbuf   int      `json:"sndbuf"`
	Stats    *float64 `json:"stats"`
	Size     int      `json:"size"`
	Bitrate  string   `json:"bitrate"`
}

type SendChannelCfg struct {
	Name     string `json:"name"`
	Dest     string `json:"dest"`
	File     string `json:"file"`
	Loop     *bool  `json:"loop"`
	Stdin    bool   `json:"stdin"`
	Bitrate  string `json:"bitrate"`
	Size     int    `json:"size"`
	Iface    string `json:"iface"`
	TTL      *int   `json:"ttl"`
	Loopback *bool  `json:"loopback"`
	Sndbuf   *int   `json:"sndbuf"`
}

type SendConfig struct {
	Defaults SendDefaults     `json:"defaults"`
	Channels []SendChannelCfg `json:"channels"`
}

// SendCfg = config efectiva de un canal de emisión.
type SendCfg struct {
	Name     string
	Dest     string
	Iface    string
	TTL      int
	Loopback bool
	Sndbuf   int
	Bitrate  int // bits por segundo
	Size     int // bytes de payload por datagrama
	File     string
	Loop     bool // repetir el fichero al llegar al final
	Stdin    bool
}

func (e SendCfg) chanName() string { return e.Name }
func (e SendCfg) key() string      { b, _ := json.Marshal(e); return string(b) }

func (e SendCfg) describe() string {
	origen := txt.logSendPattern
	switch {
	case e.File != "":
		origen = e.File
		if e.Loop {
			origen += " " + txt.logSendLooping
		}
	case e.Stdin:
		origen = "stdin"
	}
	return fmt.Sprintf("%s -> %s  (iface=%s ttl=%d %.2f Mbps, %d B)",
		origen, e.Dest, orDefault(e.Iface, "auto"), e.TTL,
		float64(e.Bitrate)/1e6, e.Size)
}

// tsPacket es el tamaño de un paquete de transport stream. Todo el mundo del
// vídeo por IP gira alrededor de este número: 1316 = 7 × 188 es el payload
// clásico precisamente porque cabe entero en una MTU de 1500.
const tsPacket = 188

// looksLikeTS reconoce un transport stream por sus bytes de sincronismo: un TS
// bien formado lleva 0x47 al principio de cada paquete de 188 bytes.
//
// Sirve para avisar solo cuando el aviso aplica: mcast-send emite cualquier
// cosa, así que un tamaño que no sea múltiplo de 188 es perfectamente legítimo
// si lo que mandas no es TS.
func looksLikeTS(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, tsPacket*4)
	n, _ := io.ReadFull(f, buf)
	if n < tsPacket*2 { // con un solo paquete no hay patrón que confirmar
		return false
	}
	for off := 0; off < n; off += tsPacket {
		if buf[off] != 0x47 {
			return false
		}
	}
	return true
}

// maxBitrate es un tope de cordura: 100 Gbps. Sirve sobre todo para que el
// valor quepa holgadamente en un int de 32 bits y no se convierta en negativo.
const maxBitrate = 100e9

// parseBitrate acepta bits por segundo a secas o con sufijo: "10M", "512k",
// "2.5M". Es lo que la gente escribe, y evita contar ceros en el JSON.
//
// La validación se hace sobre el resultado YA multiplicado, no sobre el número
// escrito: quien pone "0.5" pensando en megabits obtendría 0 bits/s al truncar,
// y con bitrate 0 el emisor se queda sin pacing y manda a velocidad de cable.
// Lo mismo con "1e19", que desborda int y sale negativo.
func parseBitrate(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("vacío")
	}
	mult := 1.0
	switch s[len(s)-1] {
	case 'k', 'K':
		mult, s = 1e3, s[:len(s)-1]
	case 'm', 'M':
		mult, s = 1e6, s[:len(s)-1]
	case 'g', 'G':
		mult, s = 1e9, s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, err
	}
	bps := v * mult
	if math.IsNaN(bps) || math.IsInf(bps, 0) || bps < 1 || bps > maxBitrate {
		return 0, fmt.Errorf("fuera de rango")
	}
	return int(bps), nil
}

func loadSendConfig(path string) (SendConfig, []string, error) {
	var c SendConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return c, nil, err
	}
	var warns []string
	strict := json.NewDecoder(bytes.NewReader(data))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&SendConfig{}); err != nil && strings.Contains(err.Error(), "unknown field") {
		warns = append(warns, fmt.Sprintf(txt.warnUnknownField, err))
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, warns, fmt.Errorf(txt.errBadJSON, err)
	}
	return c, warns, nil
}

type sendResolved struct {
	channels []SendCfg
	stats    float64
	warns    []string
	rejected map[string]bool
}

// resolveSendChannels aplica defaults y valida, con los mismos criterios que el
// relé: lo que no puede funcionar se rechaza con un aviso en vez de arrancar y
// quedarse mudo.
func resolveSendChannels(c SendConfig, statsFlag float64) sendResolved {
	d := c.Defaults
	if d.TTL == 0 {
		d.TTL = defTTL
	}
	if d.Size == 0 {
		d.Size = defSize
	}
	if d.Bitrate == "" {
		d.Bitrate = defBitrate
	}
	dLoopback := true
	if d.Loopback != nil {
		dLoopback = *d.Loopback
	}
	stats := statsFlag
	if d.Stats != nil {
		stats = *d.Stats
	}

	r := sendResolved{rejected: map[string]bool{}}
	if clamped := seconds(stats).Seconds(); stats > 0 && clamped != stats {
		r.warns = append(r.warns, fmt.Sprintf(txt.warnStatsRange, stats, clamped))
		stats = clamped
	}
	r.stats = stats

	seen := map[string]bool{}
	dests := map[string]bool{}
	stdinTaken := false
	for _, ch := range c.Channels {
		name := ch.Name
		if name == "" {
			name = strings.TrimSpace(ch.Dest)
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
		if ch.Dest == "" {
			reject(txt.warnNoDest, name)
			continue
		}
		da, err := net.ResolveUDPAddr("udp4", ch.Dest)
		if err != nil || da.IP == nil {
			reject(txt.warnBadDest, name, ch.Dest)
			continue
		}
		if da.Port == 0 {
			reject(txt.warnPortZero, name, ch.Dest)
			continue
		}
		// Dos canales al mismo destino se pisarían el flujo mutuamente: llegan
		// entrelazados y el receptor ve un TS corrupto.
		if dests[da.String()] {
			reject(txt.warnDupSendDest, name, da.String())
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
		size := d.Size
		if ch.Size != 0 {
			size = ch.Size
		}
		// 8 bytes es lo que ocupa el número de secuencia del patrón; por arriba,
		// el límite del datagrama IPv4 menos cabeceras.
		if size < 8 || size > 65507 {
			reject(txt.warnBadSize, name, size)
			continue
		}
		rate := d.Bitrate
		if ch.Bitrate != "" {
			rate = ch.Bitrate
		}
		bps, err := parseBitrate(rate)
		if err != nil {
			reject(txt.warnBadBitrate, name, rate)
			continue
		}
		if ch.File != "" && ch.Stdin {
			reject(txt.warnFileAndStdin, name)
			continue
		}
		// Solo un canal puede leer de stdin: dos se repartirían trozos alternos
		// de la misma entrada y los dos flujos saldrían corruptos, cada uno a su
		// bitrate nominal y con cero errores en las estadísticas.
		if ch.Stdin {
			if stdinTaken {
				reject(txt.warnTwoStdin, name)
				continue
			}
			stdinTaken = true
		}
		if ch.File != "" {
			fi, err := os.Stat(ch.File)
			if err != nil {
				reject(txt.warnNoFile, name, ch.File)
				continue
			}
			if fi.Size() == 0 {
				reject(txt.warnEmptyFile, name, ch.File)
				continue
			}
			// Si es un TS, la alineación importa y romperla no da error en
			// ningún sitio: el flujo sale a su bitrate, con cero errores en las
			// estadísticas, y sencillamente no hay decodificador que lo lea.
			// Son avisos y no rechazos: emitir otra cosa que no sea TS es un uso
			// legítimo, y por eso solo se avisa cuando el fichero parece TS.
			if looksLikeTS(ch.File) {
				if size%tsPacket != 0 {
					r.warns = append(r.warns, fmt.Sprintf(txt.warnSizeNotTS, name, ch.File, size, tsPacket))
				}
				if fi.Size()%tsPacket != 0 {
					r.warns = append(r.warns, fmt.Sprintf(txt.warnFileNotTS, name, ch.File, fi.Size(), tsPacket))
				}
			}
		}
		loop := true
		if ch.Loop != nil {
			loop = *ch.Loop
		}
		e := SendCfg{
			Name: name, Dest: da.String(), Iface: d.Iface, TTL: ttl,
			Loopback: dLoopback, Sndbuf: d.Sndbuf, Bitrate: bps, Size: size,
			File: ch.File, Loop: loop, Stdin: ch.Stdin,
		}
		if ch.Iface != "" {
			e.Iface = ch.Iface
		}
		if ch.Loopback != nil {
			e.Loopback = *ch.Loopback
		}
		if ch.Sndbuf != nil {
			e.Sndbuf = *ch.Sndbuf
		}
		seen[name] = true
		dests[da.String()] = true
		r.channels = append(r.channels, e)
	}
	sort.Slice(r.channels, func(i, j int) bool { return r.channels[i].Name < r.channels[j].Name })
	return r
}

// sendConfigFromFlags convierte el modo flags en una SendConfig de un canal,
// para que pase por las mismas validaciones que el modo daemon.
func sendConfigFromFlags(dst, file string, stdin bool, bitrate string, size int,
	iface string, ttl int, loopback bool, sndbuf int) SendConfig {
	loop := true
	return SendConfig{Channels: []SendChannelCfg{{
		Name: "cli", Dest: strings.TrimSpace(dst), File: file, Stdin: stdin,
		Loop: &loop, Bitrate: bitrate, Size: size, Iface: iface,
		TTL: &ttl, Loopback: &loopback, Sndbuf: &sndbuf,
	}}}
}
