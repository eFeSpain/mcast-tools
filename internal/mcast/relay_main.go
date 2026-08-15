package mcast

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

// ─── main ─────────────────────────────────────────────────────────────────────

// RelayMain es el punto de entrada de mcast-dup.
func RelayMain() {
	// El idioma se resuelve antes de registrar los flags: sus descripciones y
	// la ayuda ya tienen que estar traducidas cuando flag.Parse imprima -h.
	l, err := pickLang(langFromArgs(os.Args[1:]), osLanguage())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	setLang(l)

	configPath := flag.String("config", "", txt.flagConfig)
	logfile := flag.String("logfile", "", txt.flagLogfile)
	src := flag.String("s", "", txt.flagSrc)
	dst := flag.String("d", "", txt.flagDst)
	ifaceIP := flag.String("iface", "", txt.flagIface)
	ttl := flag.Int("ttl", defTTL, txt.flagTTL)
	loop := flag.Bool("loop", true, txt.flagLoop)
	rcvbuf := flag.Int("rcvbuf", defRcvbuf, txt.flagRcvbuf)
	sndbuf := flag.Int("sndbuf", 0, txt.flagSndbuf)
	statsItv := flag.Float64("stats", defStats, txt.flagStats)
	watchdog := flag.Float64("watchdog", defWatchdog, txt.flagWatchdog)
	from := flag.String("from", "", txt.flagFrom)
	flag.String("lang", "auto", txt.flagLang) // ya leído de os.Args; aquí solo para -h

	flag.Usage = func() {
		w := flag.CommandLine.Output()
		fmt.Fprintln(w, txt.usageTitle)
		fmt.Fprintln(w, txt.usageFlagsMode)
		fmt.Fprintln(w, txt.usageFlagsCmd)
		fmt.Fprintln(w, txt.usageDaemonMode)
		fmt.Fprintln(w, "  mcast-dup -config /etc/mcast-dup.json")
		fmt.Fprintln(w, txt.usageOptions)
		flag.PrintDefaults()
		fmt.Fprintln(w, txt.usageExamples)
		fmt.Fprintln(w, "  mcast-dup -s 239.0.10.1:5000 -d 239.255.0.1:1234,239.255.1.1:1234 -iface 10.30.0.5 -ttl 8")
		fmt.Fprintln(w, "  mcast-dup -config /etc/mcast-dup.json  ", txt.usageReloadHint)
	}
	flag.Parse()

	// Logging: info->stdout, err->stderr; o ambos a -logfile.
	var infoW, errW io.Writer = os.Stdout, os.Stderr
	var logf *reopenFile
	if *logfile != "" {
		logf = &reopenFile{path: *logfile}
		if err := logf.reopen(); err != nil {
			fmt.Fprintln(os.Stderr, txt.errOpenLogfile, err)
			os.Exit(2)
		}
		defer logf.Close()
		infoW, errW = logf, logf
	}
	info := log.New(infoW, "", log.LstdFlags)
	errl := log.New(errW, "ERROR ", log.LstdFlags)

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()
	mgr := NewManager[EffCfg](rootCtx, info, errl)
	mgr.run = func(ctx context.Context, e EffCfg, st *stats) error {
		return runRelay(ctx, e, st, errl)
	}

	load := func() bool {
		cfg, cfgWarns, err := loadConfig(*configPath)
		for _, w := range cfgWarns {
			errl.Println(txt.prefixConfig, w)
		}
		if err != nil {
			errl.Println(txt.prefixConfig, err)
			return false
		}
		r := resolveChannels(cfg, *statsItv)
		for _, w := range r.warns {
			errl.Println(txt.prefixConfig, w)
		}
		if len(r.channels) == 0 {
			errl.Println(txt.prefixConfig, txt.logNoValidChannels)
			return false
		}
		// Un canal que ya está emitiendo y cuya nueva config no valida NO se
		// para: una errata al editar el JSON no debe cortar el flujo en destino.
		// Se conserva tal cual estaba y se avisa.
		desired := mgr.keepRunning(r.channels, r.rejected, relayCompatible)
		mgr.setStatsInterval(r.stats)
		mgr.apply(desired)
		return true
	}

	if *configPath != "" {
		info.Printf(txt.logDaemonMode, *configPath)
		if !load() {
			os.Exit(2)
		}
	} else {
		if *src == "" || *dst == "" {
			flag.Usage()
			os.Exit(2)
		}
		// Mismas validaciones que el modo daemon; aquí cualquier aviso es fatal
		// porque solo hay un canal y no quedaría nada que relevar.
		r := resolveChannels(
			configFromFlags(*src, *dst, *from, *ifaceIP, *ttl, *loop, *rcvbuf, *sndbuf, *watchdog), *statsItv)
		for _, w := range r.warns {
			errl.Println(w)
		}
		if len(r.channels) == 0 {
			os.Exit(2)
		}
		chans := r.channels
		// La NIC sí se comprueba al arrancar: en modo flags no tiene sentido
		// reintentar cada 3s contra una IP que no existe.
		if _, err := resolveIface(*ifaceIP); err != nil {
			errl.Println(err)
			os.Exit(2)
		}
		e := chans[0]
		info.Printf(txt.logFlagsMode, e.Source, strings.Join(e.Dest, ","))
		mgr.setStatsInterval(*statsItv)
		mgr.apply(chans)
	}

	go mgr.statsLoop()

	// Buffer holgado: el manejador puede tardar segundos en una recarga, y con
	// capacidad 1 un SIGTERM que llegara entretanto se descartaría en silencio.
	sigc := make(chan os.Signal, 8)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	for sig := range sigc {
		if sig == syscall.SIGHUP {
			// SIGHUP es también la señal convencional para reabrir el log tras
			// una rotación, se recargue o no la configuración.
			if logf != nil {
				if err := logf.reopen(); err != nil {
					errl.Println(txt.errOpenLogfile, err)
				} else {
					info.Println(txt.logLogReopened)
				}
			}
			if *configPath == "" {
				info.Println(txt.logHupIgnored)
				continue
			}
			info.Println(txt.logReloading)
			load()
			continue
		}
		// shutdown ya espera a que los canales cierren sus sockets.
		info.Printf(txt.logStopping, sig)
		mgr.shutdown()
		rootCancel()
		return
	}
}
