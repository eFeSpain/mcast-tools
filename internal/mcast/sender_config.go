package mcast

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
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
	RTP      *bool    `json:"rtp"`
	Bitrate  string   `json:"bitrate"`
}

type SendChannelCfg struct {
	Name string `json:"name"`
	Dest string `json:"dest"`
	File string `json:"file"`
	Loop *bool  `json:"loop"`
	// Exec es una orden externa cuya salida estándar se emite: normalmente un
	// ffmpeg que remultiplexa cualquier formato a MPEG-TS. Vive y muere con el
	// canal, así que se reinicia sola si se cae.
	Exec     string `json:"exec"`
	RTP      *bool  `json:"rtp"`
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
	Exec     string
	Stdin    bool
	RTP      bool
	PCR      bool // pacing guiado por el PCR del propio flujo
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
	case e.Exec != "":
		origen = e.Exec
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

// detectContainer reconoce por los primeros bytes los contenedores que NO
// pueden viajar por UDP multicast tal cual. Devuelve "" si no reconoce nada.
//
// Solo se rechaza lo que se identifica con certeza: emitir un fichero binario
// cualquiera es un uso legítimo (datos, un patrón propio, material ya
// preparado), y no se puede exigir que todo sea TS. Pero un MP4 emitido a pelo
// no lo lee nadie, y hasta ahora salía sin un solo aviso.
func detectContainer(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := make([]byte, 16)
	n, _ := io.ReadFull(f, h)
	if n < 12 {
		return ""
	}
	switch {
	case string(h[4:8]) == "ftyp":
		return "MP4/MOV"
	case h[0] == 0x1A && h[1] == 0x45 && h[2] == 0xDF && h[3] == 0xA3:
		return "Matroska/WebM"
	case string(h[0:4]) == "RIFF" && string(h[8:12]) == "AVI ":
		return "AVI"
	case string(h[0:4]) == "RIFF" && string(h[8:12]) == "WAVE":
		return "WAV"
	case h[0] == 0x00 && h[1] == 0x00 && h[2] == 0x01 && h[3] == 0xBA:
		return "MPEG-PS (VOB)"
	case string(h[0:3]) == "FLV":
		return "FLV"
	case string(h[0:4]) == "OggS":
		return "Ogg"
	case h[0] == 0x30 && h[1] == 0x26 && h[2] == 0xB2 && h[3] == 0x75:
		return "ASF/WMV"
	case string(h[0:3]) == "ID3":
		return "MP3"
	}
	return ""
}

// remuxTarget propone el nombre de salida para la orden de ffmpeg del aviso.
func remuxTarget(path string) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + ".ts"
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
		// "pcr" pide que el ritmo lo marque el reloj del propio transport
		// stream. El bitrate configurado sigue haciendo falta como estimación
		// inicial: hasta el segundo PCR no hay ritmo medido.
		porPCR := strings.EqualFold(strings.TrimSpace(rate), "pcr")
		if porPCR {
			rate = defBitrate
		}
		bps, err := parseBitrate(rate)
		if err != nil {
			reject(txt.warnBadBitrate, name, rate)
			continue
		}
		// Con RTP el datagrama crece 12 bytes: hay que comprobar que sigue
		// cabiendo, y avisar si se pasa de la MTU típica.
		if porPCR && size%tsPacket != 0 {
			reject(txt.warnPCRNeedsTS, name, size, tsPacket)
			continue
		}
		usaRTP := false
		if d.RTP != nil {
			usaRTP = *d.RTP
		}
		if ch.RTP != nil {
			usaRTP = *ch.RTP
		}
		if usaRTP && size+rtpHeaderLen > 65507 {
			reject(txt.warnBadSize, name, size+rtpHeaderLen)
			continue
		}
		// Fichero, stdin y exec son excluyentes: con dos a la vez no hay forma
		// de saber cuál gana, y elegir por el programa sería adivinar.
		fuentes := 0
		for _, usada := range []bool{ch.File != "", ch.Stdin, ch.Exec != ""} {
			if usada {
				fuentes++
			}
		}
		if fuentes > 1 {
			reject(txt.warnManySources, name)
			continue
		}
		if ch.Exec != "" {
			args := splitCommand(ch.Exec)
			if len(args) == 0 {
				reject(txt.warnExecMissing, name, ch.Exec, txt.errExecEmpty)
				continue
			}
			// Se comprueba aquí que el programa existe: si no, el canal
			// arrancaría y se caería en bucle cada tres segundos.
			if _, err := exec.LookPath(args[0]); err != nil {
				reject(txt.warnExecMissing, name, args[0], err)
				continue
			}
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
			// Un contenedor reconocible que no es TS no puede emitirse tal cual:
			// saldría a su bitrate, con cero errores, y no habría decodificador
			// capaz de leerlo. Antes pasaba sin un solo aviso.
			if c := detectContainer(ch.File); c != "" {
				reject(txt.warnNotTSFile, name, ch.File, c, ch.File, remuxTarget(ch.File))
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
			File: ch.File, Loop: loop, Exec: ch.Exec, Stdin: ch.Stdin,
			RTP: usaRTP, PCR: porPCR,
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

// sendCompatible dice si un canal conservado de una recarga anterior puede
// convivir con los que sí han validado ahora: dos canales al mismo destino
// entrelazan sus flujos, y es justo lo que la validación rechaza al arrancar.
// Sin esta comprobación bastaba con intercambiar dos destinos y equivocarse en
// el segundo canal para acabar con los dos emitiendo al mismo grupo.
func sendCompatible(cand SendCfg, with []SendCfg) bool {
	for _, e := range with {
		if e.Dest == cand.Dest {
			return false
		}
	}
	return true
}

// sendConfigFromFlags convierte el modo flags en una SendConfig de un canal,
// para que pase por las mismas validaciones que el modo daemon.
func sendConfigFromFlags(dst, file, execCmd string, stdin bool, bitrate string, size int,
	iface string, ttl int, loopback, rtp bool, sndbuf int) SendConfig {
	loop := true
	return SendConfig{Channels: []SendChannelCfg{{
		Name: "cli", Dest: strings.TrimSpace(dst), File: file, Exec: strings.TrimSpace(execCmd), Stdin: stdin,
		Loop: &loop, Bitrate: bitrate, Size: size, Iface: iface,
		RTP: &rtp,
		TTL: &ttl, Loopback: &loopback, Sndbuf: &sndbuf,
	}}}
}
