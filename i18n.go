package main

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
	usageTitle      string
	usageFlagsMode  string
	usageFlagsCmd   string
	usageDaemonMode string
	usageOptions    string
	usageExamples   string
	usageReloadHint string

	// Descripción de cada flag
	flagConfig  string
	flagLogfile string
	flagSrc     string
	flagDst     string
	flagIface   string
	flagTTL     string
	flagLoop    string
	flagRcvbuf  string
	flagSndbuf  string
	flagStats   string
	flagLang    string

	// Validación de la configuración
	warnDupChannel   string
	warnNoSourceDest string
	warnBadSource    string
	warnNotMulticast string
	warnBadDest      string
	warnFeedbackLoop string

	// Errores de red
	errNoIface  string
	errBindRx   string
	errSource   string
	errDest     string
	errJoin     string
	errTxSocket string
	errRecv     string
	errBadJSON  string

	// Ciclo de vida de los canales
	logPanic       string
	logRelayDown   string
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
	prefixConfig       string
}

var msgsEN = msgs{
	usageTitle:      "mcast-dup — duplicates UDP multicast streams from one group to others, without re-encoding.",
	usageFlagsMode:  "\nSingle channel (flags):",
	usageFlagsCmd:   "  mcast-dup -s GROUP:PORT -d DEST1[,DEST2...] [options]",
	usageDaemonMode: "\nDaemon mode (multi-channel, reload with SIGHUP):",
	usageOptions:    "\nOptions:",
	usageExamples:   "\nExamples:",
	usageReloadHint: "(reload: kill -HUP <pid>)",

	flagConfig:  "path to the JSON config (multi-channel daemon mode)",
	flagLogfile: "write logs to this file (default: stdout/stderr)",
	flagSrc:     "flags mode: source GROUP:PORT",
	flagDst:     "flags mode: destination(s) GROUP:PORT, comma-separated",
	flagIface:   "local IP of the NIC (rx/tx)",
	flagTTL:     "outgoing multicast TTL",
	flagLoop:    "outgoing multicast loopback",
	flagRcvbuf:  "receive buffer (bytes)",
	flagSndbuf:  "send buffer (bytes; 0 = leave untouched)",
	flagStats:   "statistics interval in seconds (0 = off)",
	flagLang:    "message language: auto, en or es",

	warnDupChannel:   "duplicate channel '%s' ignored",
	warnNoSourceDest: "channel '%s' has no source/dest, ignored",
	warnBadSource:    "channel '%s' has an invalid source (%s)",
	warnNotMulticast: "channel '%s' source %s is not a multicast address, ignored",
	warnBadDest:      "channel '%s' has an invalid destination (%s)",
	warnFeedbackLoop: "channel '%s' creates a feedback loop (%s feeds back into %s), ignored",

	errNoIface:  "no interface with IP %s",
	errBindRx:   "rx bind: %w",
	errSource:   "source %s: %w",
	errDest:     "destination %s: %w",
	errJoin:     "join %s: %w",
	errTxSocket: "tx socket: %w",
	errRecv:     "recv: %w",
	errBadJSON:  "invalid JSON: %w",

	logPanic:       "[%s] recovered PANIC: %v",
	logRelayDown:   "[%s] relay down (%v); retrying in 3s",
	logStopped:     "stopped",
	logRestarting:  "restarting (config changed)",
	logStarting:    "[%s] starting  %s -> %s  (iface=%s ttl=%d)",
	logStopTimeout: "[%s] did not stop within %s; may duplicate traffic for an instant",

	errOpenLogfile:     "cannot open logfile:",
	logDaemonMode:      "daemon mode · config %s",
	logFlagsMode:       "flags mode · %s -> %s",
	logNoValidChannels: "no valid channels",
	logReloading:       "SIGHUP: reloading configuration…",
	logStopping:        "stopping…",
	prefixConfig:       "config:",
}

var msgsES = msgs{
	usageTitle:      "mcast-dup — duplica flujos multicast UDP de un grupo a otro(s), sin recodificar.",
	usageFlagsMode:  "\nModo flags (un canal):",
	usageFlagsCmd:   "  mcast-dup -s GRUPO:PUERTO -d DEST1[,DEST2...] [opciones]",
	usageDaemonMode: "\nModo daemon (multi-canal, recarga con SIGHUP):",
	usageOptions:    "\nOpciones:",
	usageExamples:   "\nEjemplos:",
	usageReloadHint: "(recargar: kill -HUP <pid>)",

	flagConfig:  "ruta a la config JSON (modo daemon multi-canal)",
	flagLogfile: "escribir los logs a este fichero (def: stdout/stderr)",
	flagSrc:     "modo flags: origen GRUPO:PUERTO",
	flagDst:     "modo flags: destino(s) GRUPO:PUERTO separados por comas",
	flagIface:   "IP local de la NIC (rx/tx)",
	flagTTL:     "TTL multicast de salida",
	flagLoop:    "loopback multicast de salida",
	flagRcvbuf:  "buffer de recepción (bytes)",
	flagSndbuf:  "buffer de envío (bytes; 0 = no tocar)",
	flagStats:   "intervalo de estadísticas en segundos (0 = ninguno)",
	flagLang:    "idioma de los mensajes: auto, en o es",

	warnDupChannel:   "canal duplicado '%s' ignorado",
	warnNoSourceDest: "canal '%s' sin source/dest, ignorado",
	warnBadSource:    "canal '%s' source inválido (%s)",
	warnNotMulticast: "canal '%s' source %s no es una dirección multicast, ignorado",
	warnBadDest:      "canal '%s' destino inválido (%s)",
	warnFeedbackLoop: "canal '%s' crea un bucle de realimentación (%s realimenta a %s), ignorado",

	errNoIface:  "no hay interfaz con IP %s",
	errBindRx:   "bind rx: %w",
	errSource:   "source %s: %w",
	errDest:     "destino %s: %w",
	errJoin:     "join %s: %w",
	errTxSocket: "socket tx: %w",
	errRecv:     "recv: %w",
	errBadJSON:  "JSON inválido: %w",

	logPanic:       "[%s] PANIC recuperado: %v",
	logRelayDown:   "[%s] relé caído (%v); reintento en 3s",
	logStopped:     "detenido",
	logRestarting:  "reiniciando (config cambiada)",
	logStarting:    "[%s] arrancando  %s -> %s  (iface=%s ttl=%d)",
	logStopTimeout: "[%s] no ha parado en %s; puede duplicar tráfico un instante",

	errOpenLogfile:     "no se pudo abrir el logfile:",
	logDaemonMode:      "modo daemon · config %s",
	logFlagsMode:       "modo flags · %s -> %s",
	logNoValidChannels: "ningún canal válido",
	logReloading:       "SIGHUP: recargando configuración…",
	logStopping:        "parando…",
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

// langFromArgs busca -lang/--lang en los argumentos antes de que se registren
// los flags: la ayuda se imprime dentro de flag.Parse y para entonces las
// descripciones ya tienen que estar en el idioma correcto.
func langFromArgs(args []string) string {
	for i, a := range args {
		name, value, hasValue := strings.Cut(a, "=")
		if name != "-lang" && name != "--lang" {
			continue
		}
		if hasValue {
			return value
		}
		if i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
