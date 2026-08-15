package mcast

import (
	"fmt"
	"strings"
)

// El idioma se decide una sola vez al arrancar, a partir del sistema operativo
// (español si el sistema está en español, inglés en cualquier otro caso) y se
// puede forzar con -lang. A partir de ahí txt es de solo lectura, así que no
// hace falta sincronizarlo entre goroutines.

type lang int

const (
	langEN lang = iota
	langES
)

// msgs reúne todo el texto que ve el usuario. Es un struct y no un mapa a
// propósito: una clave mal escrita no compila, en vez de imprimir un hueco.
type msgs struct {
	// Ayuda (-h)
	usageTitle        string
	usageFlagsMode    string
	usageFlagsCmd     string
	usageDaemonMode   string
	usageOptions      string
	usageExamples     string
	usageReloadHint   string
	usageTitleSend    string
	usageFlagsCmdSend string

	// Descripción de cada flag
	flagConfig       string
	flagLogfile      string
	flagSrc          string
	flagDst          string
	flagIface        string
	flagTTL          string
	flagLoop         string
	flagRcvbuf       string
	flagSndbuf       string
	flagStats        string
	flagLang         string
	flagWatchdog     string
	flagFrom         string
	flagSendDest     string
	flagSendFile     string
	flagSendStdin    string
	flagSendBitrate  string
	flagSendSize     string
	flagSendLoopFile string
	flagSendLoopback string
	flagSendExec     string
	flagSendRTP      string

	// Validación de la configuración
	warnDupChannel   string
	warnNoSourceDest string
	warnBadSource    string
	warnNotMulticast string
	warnBadDest      string
	warnFeedbackLoop string
	warnBadTTL       string
	warnKeptRunning  string
	warnCannotKeep   string
	warnPortZero     string
	warnStatsRange   string
	warnUnknownField string
	warnDupDest      string
	warnBadFrom      string
	warnNoDest       string
	warnDupSendDest  string
	warnBadSize      string
	warnBadBitrate   string
	warnFileAndStdin string
	warnNoFile       string
	warnSizeNotTS    string
	warnFileNotTS    string
	warnEmptyFile    string
	warnTwoStdin     string
	warnManySources  string
	warnNotTSFile    string
	warnExecMissing  string
	warnPCRNeedsTS   string
	warnNoPCR        string

	// Errores de red
	errNoIface          string
	errIfaceDown        string
	errIfaceNoMulticast string
	errBindRx           string
	errSource           string
	errDest             string
	errJoin             string
	errTxSocket         string
	errRecv             string
	errBadJSON          string
	errPanic            string
	errStalled          string
	errSockopt          string
	errPayload          string
	errBadRate          string
	errExecEmpty        string
	errExecFailed       string

	// Avisos de socket
	warnNoDstFilter  string
	warnSockbuf      string
	warnNoSSM        string
	logSSMJoined     string
	logLastDrop      string
	logClockRebase   string
	logChannelDone   string
	logAllDone       string
	logSendPattern   string
	logSendLooping   string
	logFlagsModeSend string

	// Análisis del transport stream
	logTSProgram    string
	logTSHealth     string
	logTSContents   string
	logTSNotTS      string
	logTSMisaligned string
	logTSContinuity string
	logTSTEI        string
	logTSPCRJump    string
	logTSPCRGap     string
	logTSPATGap     string
	logTSNoPCR      string
	logTSPCRNoExt   string
	logTSScrambled  string

	// Ciclo de vida de los canales
	logRelayDown   string
	logLastSendErr string
	logStopped     string
	logRestarting  string
	logStarting    string
	logStopTimeout string

	// Arranque y parada
	errOpenLogfile     string
	logDaemonMode      string
	logFlagsMode       string
	logNoValidChannels string
	logReloading       string
	logStopping        string
	logHupIgnored      string
	logLogReopened     string
	prefixConfig       string
}

var msgsEN = msgs{
	usageTitle:        "mcast-dup — duplicates UDP multicast streams from one group to others, without re-encoding.",
	usageFlagsMode:    "\nSingle channel (flags):",
	usageFlagsCmd:     "  mcast-dup -s GROUP:PORT -d DEST1[,DEST2...] [options]",
	usageDaemonMode:   "\nDaemon mode (multi-channel, reload with SIGHUP):",
	usageOptions:      "\nOptions:",
	usageExamples:     "\nExamples:",
	usageReloadHint:   "(reload: kill -HUP <pid>)",
	usageTitleSend:    "mcast-send — emits a UDP multicast stream at a controlled bitrate: a file, stdin or a generated pattern.",
	usageFlagsCmdSend: "  mcast-send -d GROUP:PORT [-f file.ts | -stdin] [-b 10M] [options]",

	flagConfig:       "path to the JSON config (multi-channel daemon mode)",
	flagLogfile:      "write logs to this file (default: stdout/stderr)",
	flagSrc:          "flags mode: source GROUP:PORT",
	flagDst:          "flags mode: destination(s) GROUP:PORT, comma-separated",
	flagIface:        "NIC for rx/tx: local IP or interface name",
	flagTTL:          "outgoing multicast TTL",
	flagLoop:         "outgoing multicast loopback",
	flagRcvbuf:       "receive buffer (bytes)",
	flagSndbuf:       "send buffer (bytes; 0 = leave untouched)",
	flagStats:        "statistics interval in seconds (0 = off)",
	flagLang:         "message language: auto, en or es",
	flagWatchdog:     "seconds without traffic before rebuilding the socket (0 = off)",
	flagFrom:         "only accept the group from these senders (SSM), comma-separated",
	flagSendDest:     "flags mode: destination GROUP:PORT",
	flagSendFile:     "file to emit (looped by default)",
	flagSendStdin:    "read what to emit from standard input",
	flagSendBitrate:  "bitrate: bits/s, a suffix like 10M or 512k, or \"pcr\" to follow the stream own clock",
	flagSendSize:     "payload bytes per datagram (1316 = 7 TS packets)",
	flagSendLoopFile: "restart the file when it ends",
	flagSendLoopback: "outgoing multicast loopback",
	flagSendExec:     "run this command and emit its standard output (e.g. an ffmpeg that remuxes to MPEG-TS)",
	flagSendRTP:      "wrap each datagram in an RTP header (RFC 3550, PT 33): needed by SMPTE 2022-2 and many professional decoders",

	warnDupChannel:   "duplicate channel '%s' ignored",
	warnNoSourceDest: "channel '%s' has no source/dest, ignored",
	warnBadSource:    "channel '%s' has an invalid source (%s)",
	warnNotMulticast: "channel '%s' source %s is not a multicast address, ignored",
	warnBadDest:      "channel '%s' has an invalid destination (%s)",
	warnFeedbackLoop: "channel '%s' creates a feedback loop (%s feeds back into %s), ignored",
	warnBadTTL:       "channel '%s' has ttl %d out of range [0,255], ignored",
	warnKeptRunning:  "channel '%s' was rejected on reload; the one already running stays untouched",
	warnCannotKeep:   "channel '%s' was rejected on reload and cannot be kept running either: its previous settings clash with the ones that did validate; stopped",
	warnPortZero:     "channel '%s' address %s has no port (or port 0): it would never receive or send anything, ignored",
	warnStatsRange:   "stats interval %v is out of range, using %v",
	warnUnknownField: "unknown field ignored (%v)",
	warnDupDest:      "channel '%s' repeats destination %s, the copy is ignored",
	warnBadFrom:      "channel '%s' has an invalid source filter in 'from' (%s): it must be the unicast address of the sender, ignored",
	warnNoDest:       "channel '%s' has no dest, ignored",
	warnDupSendDest:  "channel '%s' emits to %s, already used by another channel: the two streams would interleave, ignored",
	warnBadSize:      "channel '%s' has an unusable datagram size (%d bytes), ignored",
	warnBadBitrate:   "channel '%s' has an invalid bitrate (%s): use bits/s or a suffix like 10M or 512k, ignored",
	warnFileAndStdin: "channel '%s' asks for both a file and stdin, ignored",
	warnNoFile:       "channel '%s' cannot read %s, ignored",
	warnSizeNotTS:    "channel '%s': %s looks like MPEG-TS, but the datagram size (%d) is not a multiple of %d: TS packets would be split across datagrams and no decoder could read the stream",
	warnFileNotTS:    "channel '%s': %s looks like MPEG-TS, but its length (%d bytes) is not a multiple of %d: every loop would emit a truncated packet",
	warnEmptyFile:    "channel '%s': %s is empty, there is nothing to emit; ignored",
	warnTwoStdin:     "channel '%s' also reads from stdin, and only one channel can: both streams would get alternate chunks; ignored",
	warnManySources:  "channel '%s' asks for more than one source (file, stdin or exec); pick one, ignored",
	warnNotTSFile:    "channel '%s': %s is %s, and UDP multicast carries MPEG-TS. Remux it without re-encoding:  ffmpeg -i %s -c copy -f mpegts %s",
	warnExecMissing:  "channel '%s' cannot run %s (%v), ignored",
	warnPCRNeedsTS:   "channel '%s' asks for PCR pacing, but a datagram size of %d is not a multiple of %d: the TS packets would be split and no PCR could be read; ignored",
	warnNoPCR:        "[%s] no PCR found in the stream: it is not MPEG-TS, or it carries none. Falling back to the configured %.2f Mbps",

	errNoIface:          "no interface named or addressed %s",
	errIfaceDown:        "interface %s is down",
	errIfaceNoMulticast: "interface %s does not support multicast",
	errBindRx:           "rx bind: %w",
	errSource:           "source %s: %w",
	errDest:             "destination %s: %w",
	errJoin:             "join %s: %w",
	errTxSocket:         "tx socket: %w",
	errRecv:             "recv: %w",
	errBadJSON:          "invalid JSON: %w",
	errPanic:            "recovered PANIC: %v",
	errStalled:          "no traffic for %s",
	errSockopt:          "socket option %s: %w",
	errPayload:          "payload: %w",
	errBadRate:          "unusable bitrate/size (%d bps, %d B): with no clock it would emit at line rate",
	errExecEmpty:        "the exec command is empty",
	errExecFailed:       "the source command failed (%v); its last output: %s",

	warnNoDstFilter: "[%s] this platform cannot filter by destination address: foreign unicast or broadcast traffic reaching port %d will be relayed too",
	warnSockbuf:     "[%s] could not set %s to %d bytes: %v",
	warnNoSSM:       "[%s] source-specific join unavailable (%v): joining the whole group and filtering senders here instead, which works but does not stop the traffic upstream",
	logSSMJoined:    "[%s] source-specific join (SSM) for %s from %s",
	logLastDrop:     "[%s] last discarded datagram: %s",

	logTSProgram:     "programme %d: PCR on PID %d · %s",
	logTSContents:    "[%s] carries %s",
	logTSHealth:      "[%s] TS: %s",
	logTSNotTS:       "%d datagrams are not MPEG-TS",
	logTSMisaligned:  "%d datagrams are not a whole number of 188-byte packets",
	logTSContinuity:  "%d continuity errors (worst PID %d)",
	logTSTEI:         "%d packets flagged as corrupt upstream (TEI)",
	logTSPCRJump:     "%d PCR jumps with no discontinuity_indicator",
	logTSPCRGap:      "%d PCR intervals above the limit, peak %.0f ms (max %d)",
	logTSPATGap:      "%d PAT intervals above the limit, peak %.0f ms (max %d)",
	logTSNoPCR:       "no PCR: nothing to pace or lock to",
	logTSPCRNoExt:    "PCR with no 27 MHz extension: clock quantised to 90 kHz",
	logTSScrambled:   "%d scrambled packets",
	logClockRebase:   "clock rebased, it was %s behind: the source is not keeping up with the requested bitrate",
	logChannelDone:   "[%s] channel finished: source exhausted",
	logAllDone:       "every channel has finished; nothing left to emit",
	logSendPattern:   "generated pattern",
	logSendLooping:   "(looping)",
	logFlagsModeSend: "flags mode · %s",

	logRelayDown:   "[%s] relay down (%v); retrying in %s",
	logLastSendErr: "[%s] last send error: %v",
	logStopped:     "stopped",
	logRestarting:  "restarting (config changed)",
	logStarting:    "[%s] starting  %s",
	logStopTimeout: "channels still not stopped after %s: %s; they may duplicate traffic for an instant",

	errOpenLogfile:     "cannot open logfile:",
	logDaemonMode:      "daemon mode · config %s",
	logFlagsMode:       "flags mode · %s -> %s",
	logNoValidChannels: "no valid channels",
	logReloading:       "SIGHUP: reloading configuration…",
	logStopping:        "stopping (%v)…",
	logHupIgnored:      "SIGHUP ignored: nothing to reload in flags mode",
	logLogReopened:     "log file reopened",
	prefixConfig:       "config:",
}

var msgsES = msgs{
	usageTitle:        "mcast-dup — duplica flujos multicast UDP de un grupo a otro(s), sin recodificar.",
	usageFlagsMode:    "\nModo flags (un canal):",
	usageFlagsCmd:     "  mcast-dup -s GRUPO:PUERTO -d DEST1[,DEST2...] [opciones]",
	usageDaemonMode:   "\nModo daemon (multi-canal, recarga con SIGHUP):",
	usageOptions:      "\nOpciones:",
	usageExamples:     "\nEjemplos:",
	usageReloadHint:   "(recargar: kill -HUP <pid>)",
	usageTitleSend:    "mcast-send — emite un flujo multicast UDP a un ritmo controlado: un fichero, stdin o un patrón generado.",
	usageFlagsCmdSend: "  mcast-send -d GRUPO:PUERTO [-f fichero.ts | -stdin] [-b 10M] [opciones]",

	flagConfig:       "ruta a la config JSON (modo daemon multi-canal)",
	flagLogfile:      "escribir los logs a este fichero (def: stdout/stderr)",
	flagSrc:          "modo flags: origen GRUPO:PUERTO",
	flagDst:          "modo flags: destino(s) GRUPO:PUERTO separados por comas",
	flagIface:        "NIC para rx/tx: IP local o nombre de la interfaz",
	flagTTL:          "TTL multicast de salida",
	flagLoop:         "loopback multicast de salida",
	flagRcvbuf:       "buffer de recepción (bytes)",
	flagSndbuf:       "buffer de envío (bytes; 0 = no tocar)",
	flagStats:        "intervalo de estadísticas en segundos (0 = ninguno)",
	flagLang:         "idioma de los mensajes: auto, en o es",
	flagWatchdog:     "segundos sin tráfico antes de rehacer el socket (0 = desactivado)",
	flagFrom:         "aceptar el grupo solo de estos emisores (SSM), separados por comas",
	flagSendDest:     "modo flags: destino GRUPO:PUERTO",
	flagSendFile:     "fichero a emitir (en bucle por defecto)",
	flagSendStdin:    "leer de la entrada estándar lo que hay que emitir",
	flagSendBitrate:  "bitrate: bits/s, con sufijo (10M, 512k) o \"pcr\" para seguir el reloj del propio flujo",
	flagSendSize:     "bytes de payload por datagrama (1316 = 7 paquetes TS)",
	flagSendLoopFile: "volver a empezar el fichero al terminarlo",
	flagSendLoopback: "loopback multicast de salida",
	flagSendExec:     "ejecutar esta orden y emitir su salida estándar (por ejemplo un ffmpeg que remultiplexe a MPEG-TS)",
	flagSendRTP:      "encapsular cada datagrama en RTP (RFC 3550, PT 33): lo necesitan SMPTE 2022-2 y muchos decodificadores profesionales",

	warnDupChannel:   "canal duplicado '%s' ignorado",
	warnNoSourceDest: "canal '%s' sin source/dest, ignorado",
	warnBadSource:    "canal '%s' source inválido (%s)",
	warnNotMulticast: "canal '%s' source %s no es una dirección multicast, ignorado",
	warnBadDest:      "canal '%s' destino inválido (%s)",
	warnFeedbackLoop: "canal '%s' crea un bucle de realimentación (%s realimenta a %s), ignorado",
	warnBadTTL:       "canal '%s' tiene ttl %d fuera del rango [0,255], ignorado",
	warnKeptRunning:  "canal '%s' rechazado en la recarga; se mantiene en marcha el que ya estaba",
	warnCannotKeep:   "canal '%s' rechazado en la recarga y tampoco se puede mantener: su configuración anterior choca con las que sí han validado; detenido",
	warnPortZero:     "canal '%s': la dirección %s no lleva puerto (o es el 0), no recibiría ni enviaría nada; ignorado",
	warnStatsRange:   "el intervalo de stats %v está fuera de rango, se usa %v",
	warnUnknownField: "campo desconocido ignorado (%v)",
	warnDupDest:      "canal '%s' repite el destino %s, se ignora la copia",
	warnBadFrom:      "canal '%s' tiene un filtro de origen inválido en 'from' (%s): tiene que ser la dirección unicast del emisor; ignorado",
	warnNoDest:       "canal '%s' sin dest, ignorado",
	warnDupSendDest:  "canal '%s' emite a %s, que ya usa otro canal: los dos flujos se entrelazarían; ignorado",
	warnBadSize:      "canal '%s' tiene un tamaño de datagrama inservible (%d bytes), ignorado",
	warnBadBitrate:   "canal '%s' tiene un bitrate inválido (%s): usa bits/s o un sufijo como 10M o 512k; ignorado",
	warnFileAndStdin: "canal '%s' pide fichero y stdin a la vez, ignorado",
	warnNoFile:       "canal '%s' no puede leer %s, ignorado",
	warnSizeNotTS:    "canal '%s': %s parece MPEG-TS, pero el tamaño de datagrama (%d) no es múltiplo de %d: los paquetes TS saldrían partidos entre datagramas y ningún decodificador podría leer el flujo",
	warnFileNotTS:    "canal '%s': %s parece MPEG-TS, pero su longitud (%d bytes) no es múltiplo de %d: cada vuelta del bucle emitiría un paquete cortado",
	warnEmptyFile:    "canal '%s': %s está vacío, no hay nada que emitir; ignorado",
	warnTwoStdin:     "canal '%s' también lee de stdin, y solo puede hacerlo uno: los dos flujos recibirían trozos alternos; ignorado",
	warnManySources:  "canal '%s' pide más de una fuente (fichero, stdin o exec); elige una, ignorado",
	warnNotTSFile:    "canal '%s': %s es %s, y por UDP multicast viaja MPEG-TS. Remultiplexa sin recodificar:  ffmpeg -i %s -c copy -f mpegts %s",
	warnExecMissing:  "canal '%s' no puede ejecutar %s (%v), ignorado",
	warnPCRNeedsTS:   "canal '%s' pide pacing por PCR, pero un datagrama de %d no es múltiplo de %d: los paquetes TS saldrían partidos y no se podría leer ningún PCR; ignorado",
	warnNoPCR:        "[%s] no se ha encontrado ningún PCR en el flujo: o no es MPEG-TS, o no los lleva. Se vuelve a los %.2f Mbps configurados",

	errNoIface:          "no hay ninguna interfaz llamada ni direccionada %s",
	errIfaceDown:        "la interfaz %s está caída",
	errIfaceNoMulticast: "la interfaz %s no admite multicast",
	errBindRx:           "bind rx: %w",
	errSource:           "source %s: %w",
	errDest:             "destino %s: %w",
	errJoin:             "join %s: %w",
	errTxSocket:         "socket tx: %w",
	errRecv:             "recv: %w",
	errBadJSON:          "JSON inválido: %w",
	errPanic:            "PANIC recuperado: %v",
	errStalled:          "sin tráfico durante %s",
	errSockopt:          "opción de socket %s: %w",
	errPayload:          "origen de datos: %w",
	errBadRate:          "bitrate/tamaño inservibles (%d bps, %d B): sin reloj emitiría a velocidad de cable",
	errExecEmpty:        "la orden de exec está vacía",
	errExecFailed:       "la orden de origen ha fallado (%v); su última salida: %s",

	warnNoDstFilter: "[%s] esta plataforma no puede filtrar por dirección de destino: el tráfico unicast o broadcast ajeno que llegue al puerto %d también se reenviará",
	warnSockbuf:     "[%s] no se pudo fijar %s a %d bytes: %v",
	warnNoSSM:       "[%s] no hay join por fuente disponible (%v): se une al grupo entero y se filtran los emisores aquí, lo que funciona pero no corta el tráfico aguas arriba",
	logSSMJoined:    "[%s] join por fuente (SSM) a %s desde %s",
	logLastDrop:     "[%s] último datagrama descartado: %s",

	logTSProgram:     "programa %d: PCR en el PID %d · %s",
	logTSContents:    "[%s] lleva %s",
	logTSHealth:      "[%s] TS: %s",
	logTSNotTS:       "%d datagramas no son MPEG-TS",
	logTSMisaligned:  "%d datagramas no traen paquetes enteros de 188 bytes",
	logTSContinuity:  "%d errores de continuidad (peor PID %d)",
	logTSTEI:         "%d paquetes marcados como corruptos aguas arriba (TEI)",
	logTSPCRJump:     "%d saltos de PCR sin discontinuity_indicator",
	logTSPCRGap:      "%d intervalos de PCR por encima del límite, máx %.0f ms (tope %d)",
	logTSPATGap:      "%d intervalos de PAT por encima del límite, máx %.0f ms (tope %d)",
	logTSNoPCR:       "sin PCR: no hay reloj al que engancharse",
	logTSPCRNoExt:    "PCR sin extensión de 27 MHz: reloj cuantizado a 90 kHz",
	logTSScrambled:   "%d paquetes cifrados",
	logClockRebase:   "reloj rebasado, iba %s por detrás: la fuente no da el bitrate pedido",
	logChannelDone:   "[%s] canal terminado: fuente agotada",
	logAllDone:       "todos los canales han terminado; no queda nada que emitir",
	logSendPattern:   "patrón generado",
	logSendLooping:   "(en bucle)",
	logFlagsModeSend: "modo flags · %s",

	logRelayDown:   "[%s] relé caído (%v); reintento en %s",
	logLastSendErr: "[%s] último error de envío: %v",
	logStopped:     "detenido",
	logRestarting:  "reiniciando (config cambiada)",
	logStarting:    "[%s] arrancando  %s",
	logStopTimeout: "canales que siguen sin parar tras %s: %s; pueden duplicar tráfico un instante",

	errOpenLogfile:     "no se pudo abrir el logfile:",
	logDaemonMode:      "modo daemon · config %s",
	logFlagsMode:       "modo flags · %s -> %s",
	logNoValidChannels: "ningún canal válido",
	logReloading:       "SIGHUP: recargando configuración…",
	logStopping:        "parando (%v)…",
	logHupIgnored:      "SIGHUP ignorado: en modo flags no hay nada que recargar",
	logLogReopened:     "fichero de log reabierto",
	prefixConfig:       "config:",
}

// txt es el idioma activo. Inglés por defecto: si la detección falla, el
// mensaje lo entiende más gente.
var txt = msgsEN

func messagesFor(l lang) msgs {
	if l == langES {
		return msgsES
	}
	return msgsEN
}

func setLang(l lang) { txt = messagesFor(l) }

// pickLang decide el idioma. override viene de -lang ("", "auto", "en", "es")
// y osLang del sistema operativo.
func pickLang(override, osLang string) (lang, error) {
	switch strings.ToLower(strings.TrimSpace(override)) {
	case "es":
		return langES, nil
	case "en":
		return langEN, nil
	case "", "auto":
		if primarySubtag(osLang) == "es" {
			return langES, nil
		}
		return langEN, nil
	default:
		// Este mensaje no puede traducirse: aún no sabemos el idioma.
		return langEN, fmt.Errorf("unknown -lang value %q (use auto, en or es)", override)
	}
}

// primarySubtag extrae "es" de "es_ES.UTF-8", "es-MX" o "es". Se compara la
// subetiqueta entera para no confundir el euskera (eu) ni el estonio (et), ni
// aceptar cosas como "esperanto".
func primarySubtag(locale string) string {
	s := strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(s, "_-.@"); i >= 0 {
		s = s[:i]
	}
	return s
}

// langFromEnv aplica la precedencia POSIX habitual.
func langFromEnv(lcAll, lcMessages, lang string) string {
	for _, v := range []string{lcAll, lcMessages, lang} {
		if v != "" {
			return v
		}
	}
	return ""
}

// flagsWithValue son los flags que consumen el argumento siguiente. Hay que
// saberlo para recorrer os.Args igual que lo haría el paquete flag: sin eso, el
// valor de un flag ("-d 239.0.0.1:1") se confundiría con un posicional.
var flagsWithValue = map[string]bool{
	"config": true, "logfile": true, "s": true, "d": true, "iface": true,
	// del emisor y del filtro SSM:
	"f": true, "b": true, "size": true, "from": true,
	"ttl": true, "rcvbuf": true, "sndbuf": true, "stats": true,
	"watchdog": true, "lang": true,
}

// langFromArgs busca -lang/--lang en los argumentos antes de que se registren
// los flags: la ayuda se imprime dentro de flag.Parse y para entonces las
// descripciones ya tienen que estar en el idioma correcto.
//
// Imita al paquete flag en las dos cosas que importan: deja de mirar en "--" o
// en el primer argumento que no sea un flag, y si -lang aparece repetido se
// queda con el último.
func langFromArgs(args []string) string {
	found := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || a == "-" || !strings.HasPrefix(a, "-") {
			break
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if name == "lang" {
			switch {
			case hasValue:
				found = value
			case i+1 < len(args):
				found = args[i+1]
				i++
			}
			continue
		}
		if !hasValue && flagsWithValue[name] {
			i++ // lo siguiente es su valor, no otro flag
		}
	}
	return found
}
