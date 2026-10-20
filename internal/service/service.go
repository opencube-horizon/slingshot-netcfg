// Package service wires the slingshot-netcfg components (discovery, lldpd
// poller, reconciler) and runs them in one errgroup (spec §6.1, §9.1).
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sync/errgroup"

	"github.com/opencube-horizon/slingshot-netcfg/internal/config"
	"github.com/opencube-horizon/slingshot-netcfg/internal/discover"
	"github.com/opencube-horizon/slingshot-netcfg/internal/lldpd"
	"github.com/opencube-horizon/slingshot-netcfg/internal/netapply"
	"github.com/opencube-horizon/slingshot-netcfg/internal/reconcile"
	"github.com/opencube-horizon/slingshot-netcfg/internal/sandbox"
	"github.com/opencube-horizon/slingshot-netcfg/internal/sdnotify"
	"github.com/opencube-horizon/slingshot-netcfg/internal/status"
)

// Options configures one run.
type Options struct {
	Config config.Config
	Log    *slog.Logger
	// Only restricts selection to these interfaces (--iface).
	Only []string
	// SysRoot defaults to /sys and RunDir to /run/slingshot-netcfg; the
	// integration tests point them at temporary directories.
	SysRoot, RunDir string
	DryRun, Once    bool
	OnceTimeout     time.Duration
}

// ErrOnceTimeout reports that --once didn't see every interface provisioned in time.
var ErrOnceTimeout = errors.New("not all selected interfaces provisioned in time")

// Run runs until ctx is done (nil), a component fails (its error), or, with
// Once, until everything is provisioned (nil) or OnceTimeout passes (ErrOnceTimeout).
func Run(ctx context.Context, o Options) error {
	cfg := o.Config
	poller, err := lldpd.NewPoller(cfg.LLDPD, cfg.ResyncInterval)
	if err != nil {
		return err
	}
	handle, err := netlink.NewHandle()
	if err != nil {
		return fmt.Errorf("netlink: %w", err)
	}
	defer handle.Close()
	o = withDefaults(o)
	notifier, err := sdnotify.FromEnv()
	if err != nil {
		return err
	}
	defer func() { _ = notifier.Close() }() // a failed close of a datagram socket needs no handling
	if err := os.MkdirAll(o.RunDir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", o.RunDir, err)
	}

	once := o.Once || o.DryRun
	if once {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.OnceTimeout)
		defer cancel()
	}
	rec := &reconcile.Reconciler{
		Cfg:    cfg,
		Kernel: &netapply.Kernel{Handle: handle, Protocol: cfg.Routing.Protocol},
		Selector: discover.Selector{
			SysRoot: o.SysRoot, Pattern: cfg.Interfaces.NamePattern, PCIIDs: cfg.Interfaces.PCIIDs, Only: o.Only,
		},
		Status:   status.Writer{Dir: o.RunDir},
		Log:      o.Log,
		DryRun:   o.DryRun,
		Once:     once,
		Notify:   notifier.Notify,
		Watchdog: notifier.WatchdogInterval(),
	}
	o.Log.Info("starting", "gomaxprocs", runtime.GOMAXPROCS(0), "lldpcli", poller.Path(), "socket", cfg.LLDPD.Socket, "dryRun", o.DryRun, "once", once)
	if cfg.Security.Landlock {
		abi, err := sandbox.Restrict(sandbox.Paths{SysRoot: o.SysRoot, ProcNet: netapply.ProcNet, RunDir: o.RunDir, LLDPCLI: poller.Path()})
		if err != nil {
			return err
		}
		o.Log.Info("landlock", "abi", abi)
	}

	results := make(chan lldpd.Result, 1)
	triggers := make(chan struct{}, 1)
	fast := make(chan bool, 1)
	g, gctx := errgroup.WithContext(ctx)
	runCtx, finish := context.WithCancel(gctx)
	defer finish()
	g.Go(func() error { return poller.Run(runCtx, results, fast) })
	g.Go(func() error {
		return discover.Watch(runCtx, triggers, func(err error) {
			o.Log.Warn("netlink subscription error, resubscribing", "err", err)
		})
	})
	g.Go(func() error {
		defer finish()
		return rec.Run(runCtx, results, triggers, fast)
	})
	if err := g.Wait(); err != nil {
		return err
	}
	if once && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return ErrOnceTimeout
	}
	o.Log.Info("stopped")
	return nil
}

func withDefaults(o Options) Options {
	if o.SysRoot == "" {
		o.SysRoot = "/sys"
	}
	if o.RunDir == "" {
		o.RunDir = "/run/slingshot-netcfg"
	}
	if o.OnceTimeout == 0 {
		o.OnceTimeout = 120 * time.Second
	}
	return o
}
