package mcast

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// SendMain es el punto de entrada de mcast-send.
func SendMain() {
	// Igual que en el relé: el idioma se resuelve antes de registrar los flags,
	// porque la ayuda se imprime dentro de flag.Parse.
	l, err := pickLang(langFromArgs(os.Args[1:]), osLanguage())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	setLang(l)

	configPath := flag.String("config", "", txt.flagConfig)
	logfile := flag.String("logfile", "", txt.flagLogfile)
	dst := flag.String("d", "", txt.flagSendDest)
	file := flag.String("f", "", txt.flagSendFile)
	stdin := flag.Bool("stdin", false, txt.flagSendStdin)
	bitrate := flag.String("b", defBitrate, txt.flagSendBitrate)
	size := flag.Int("size", defSize, txt.flagSendSize)
	loopFile := flag.Bool("loop-file", true, txt.flagSendLoopFile)
	ifaceIP := flag.String("iface", "", txt.flagIface)
	ttl := flag.Int("ttl", defTTL, txt.flagTTL)
	loopback := flag.Bool("loop", true, txt.flagSendLoopback)
	sndbuf := flag.Int("sndbuf", 0, txt.flagSndbuf)
	statsItv := flag.Float64("stats", defStats, txt.flagStats)
	flag.String("lang", "auto", txt.flagLang) // ya leído de os.Args; aquí solo para -h

	flag.Usage = func() {
		w := flag.CommandLine.Output()
		fmt.Fprintln(w, txt.usageTitleSend)
		fmt.Fprintln(w, txt.usageFlagsMode)
		fmt.Fprintln(w, txt.usageFlagsCmdSend)
		fmt.Fprintln(w, txt.usageDaemonMode)
		fmt.Fprintln(w, "  mcast-send -config /etc/mcast-send.json")
		fmt.Fprintln(w, txt.usageOptions)
		flag.PrintDefaults()
		fmt.Fprintln(w, txt.usageExamples)
		fmt.Fprintln(w, "  mcast-send -d 239.0.10.1:5000 -f barras.ts -b 10M -iface eth0")
		fmt.Fprintln(w, "  ffmpeg ... -f mpegts - | mcast-send -d 239.0.10.1:5000 -stdin -b 8M")
		fmt.Fprintln(w, "  mcast-send -config /etc/mcast-send.json  ", txt.usageReloadHint)
	}
	flag.Parse()

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
	mgr := NewManager[SendCfg](rootCtx, info, errl)
	mgr.sender = true
	mgr.run = func(ctx context.Context, e SendCfg, st *stats) error {
		return runSender(ctx, e, st, errl)
	}

	load := func() bool {
		cfg, cfgWarns, err := loadSendConfig(*configPath)
		for _, w := range cfgWarns {
			errl.Println(txt.prefixConfig, w)
		}
		if err != nil {
			errl.Println(txt.prefixConfig, err)
			return false
		}
		r := resolveSendChannels(cfg, *statsItv)
		for _, w := range r.warns {
			errl.Println(txt.prefixConfig, w)
		}
		if len(r.channels) == 0 {
			errl.Println(txt.prefixConfig, txt.logNoValidChannels)
			return false
		}
		// Igual que en el relé: un canal que ya está emitiendo y cuya config
		// nueva no valida se queda como estaba en vez de cortarse.
		desired := mgr.keepRunning(r.channels, r.rejected, sendCompatible)
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
		if *dst == "" {
			flag.Usage()
			os.Exit(2)
		}
		r := resolveSendChannels(
			sendConfigFromFlags(*dst, *file, *stdin, *bitrate, *size,
				*ifaceIP, *ttl, *loopback, *sndbuf), *statsItv)
		for _, w := range r.warns {
			errl.Println(w)
		}
		if len(r.channels) == 0 {
			os.Exit(2)
		}
		if _, err := resolveIface(*ifaceIP); err != nil {
			errl.Println(err)
			os.Exit(2)
		}
		// -loop-file solo tiene sentido con fichero; sin él, el flag se ignora.
		if *file != "" {
			r.channels[0].Loop = *loopFile
		}
		info.Printf(txt.logFlagsModeSend, r.channels[0].describe())
		mgr.setStatsInterval(*statsItv)
		mgr.apply(r.channels)
	}

	go mgr.statsLoop()

	sigc := make(chan os.Signal, 8)
	signal.Notify(sigc, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	for {
		var sig os.Signal
		select {
		case sig = <-sigc:
		case <-mgr.Drained():
			// Se ha acabado el material de todos los canales: un emisor de un
			// clip sin bucle, o una tubería que se ha cerrado, no tiene por qué
			// quedarse dando vueltas.
			info.Println(txt.logAllDone)
			rootCancel()
			return
		}
		if sig == syscall.SIGHUP {
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
		info.Printf(txt.logStopping, sig)
		mgr.shutdown()
		rootCancel()
		return
	}
}
