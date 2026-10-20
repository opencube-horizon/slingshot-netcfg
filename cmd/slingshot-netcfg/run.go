package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/opencube-horizon/slingshot-netcfg/internal/config"
	"github.com/opencube-horizon/slingshot-netcfg/internal/service"
)

func runCmd(args []string) int {
	var (
		cfgPath, lldpcli, socket, level string
		ifaces                          stringList
		dryRun, once                    bool
		onceTimeout                     time.Duration
	)
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.StringVar(&cfgPath, "config", config.DefaultPath, "configuration file")
	fs.StringVar(&lldpcli, "lldpcli", "", "lldpcli binary (overrides lldpd.lldpcli)")
	fs.StringVar(&socket, "lldpd-socket", "", "lldpd control socket (overrides lldpd.socket)")
	fs.Var(&ifaces, "iface", "restrict selection to this interface (repeatable)")
	fs.BoolVar(&dryRun, "dry-run", false, "log the plan instead of executing it (implies --once)")
	fs.BoolVar(&once, "once", false, "exit once every selected interface is provisioned")
	fs.DurationVar(&onceTimeout, "once-timeout", 120*time.Second, "time limit for --once")
	fs.StringVar(&level, "log-level", "info", "debug, info, warn or error")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	lvl, err := parseLevel(level)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid --log-level %q\n", level)
		return 2
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Error("exiting", "err", err)
		return 1
	}
	if lldpcli != "" {
		cfg.LLDPD.LLDPCLI = lldpcli
	}
	if socket != "" {
		cfg.LLDPD.Socket = socket
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	err = service.Run(ctx, service.Options{Config: cfg, Log: log, Only: ifaces, DryRun: dryRun, Once: once, OnceTimeout: onceTimeout})
	if err != nil {
		log.Error("exiting", "err", err)
		return 1
	}
	return 0
}

func parseLevel(s string) (slog.Level, error) {
	var l slog.Level
	err := l.UnmarshalText([]byte(strings.ToUpper(s)))
	return l, err
}

// stringList is a repeatable flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }
