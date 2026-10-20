package reconcile

import (
	"context"
	"log/slog"
	"net/netip"
	"time"

	"github.com/opencube-horizon/slingshot-netcfg/internal/config"
	"github.com/opencube-horizon/slingshot-netcfg/internal/discover"
	"github.com/opencube-horizon/slingshot-netcfg/internal/lldpd"
	"github.com/opencube-horizon/slingshot-netcfg/internal/netapply"
	"github.com/opencube-horizon/slingshot-netcfg/internal/sdnotify"
	"github.com/opencube-horizon/slingshot-netcfg/internal/status"
)

// Kernel observes kernel state and applies operations. It is an interface
// because unit tests replace netlink with an in-memory kernel.
type Kernel interface {
	ObserveLinks() ([]netapply.Link, error)
	Observe(links []netapply.Link, devs []string, disableIPv6 bool) (netapply.State, error)
	Apply(op netapply.Op) error
}

// Reconciler runs passes (§6.1). It owns all reconcile state; nothing else
// touches it, so it needs no locks.
type Reconciler struct {
	Cfg      config.Config
	Kernel   Kernel
	Selector discover.Selector
	Status   status.Writer
	Log      *slog.Logger
	// DryRun logs each pass's plan instead of executing it.
	DryRun bool
	// Once makes Run return nil as soon as every selected interface is provisioned.
	Once bool
	// Notify, if set, receives the systemd notifications (sdnotify states).
	Notify func(state string) error
	// Watchdog, if positive, is the systemd watchdog timeout; Run pings at half of it.
	Watchdog time.Duration

	snapshot   *lldpd.Snapshot
	lldpdErr   error
	lastPoll   time.Time
	ifs        map[string]*ifaceState
	seenVLAN   map[netip.Prefix]bool
	lastDoc    string
	lastFast   bool
	sentFast   bool
	ready      bool
	lastStatus string
}

const debounce = 500 * time.Millisecond

// Run executes passes until ctx is done (returning nil), or, with Once, until
// all selected interfaces are provisioned. Passes run after each lldpd poll
// result, after a debounced trigger, and every resync interval. After each
// pass the wanted poll mode (fast while something with carrier isn't
// provisioned) is sent on fast, which keeps only the latest value, and the
// state is reported to systemd (§6.10).
func (r *Reconciler) Run(ctx context.Context, results <-chan lldpd.Result, triggers <-chan struct{}, fast chan bool) error {
	r.ifs = map[string]*ifaceState{}
	r.seenVLAN = map[netip.Prefix]bool{}
	resync := time.NewTicker(r.Cfg.ResyncInterval)
	defer resync.Stop()
	var watchdog <-chan time.Time
	if r.Watchdog > 0 {
		t := time.NewTicker(r.Watchdog / 2)
		defer t.Stop()
		watchdog = t.C
	}
	var settle <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			r.notify(sdnotify.Stopping)
			return nil
		case <-watchdog:
			r.notify(sdnotify.Watchdog)
			continue
		case res := <-results:
			r.takeResult(res, time.Now())
		case <-triggers:
			if settle == nil {
				settle = time.After(debounce)
			}
			continue
		case <-settle:
			settle = nil
		case <-resync.C:
		}
		done := r.pass(time.Now())
		r.sendFast(fast)
		r.report()
		if r.Once && (done || (r.DryRun && r.snapshot != nil)) {
			return nil
		}
	}
}

func (r *Reconciler) takeResult(res lldpd.Result, now time.Time) {
	if res.Err != nil {
		if r.lldpdErr == nil {
			r.Log.Warn("lldpd unreachable", "err", res.Err, "hint", hintLLDPD)
		}
		r.lldpdErr = res.Err
		return
	}
	if r.lldpdErr != nil {
		r.Log.Info("lldpd reachable again")
	}
	r.lldpdErr = nil
	r.snapshot = res.Snapshot
	r.lastPoll = now
}

func (r *Reconciler) sendFast(fast chan bool) {
	want := false
	for _, s := range r.ifs {
		want = want || (s.running && s.phase != PhaseProvisioned)
	}
	if r.sentFast && want == r.lastFast {
		return
	}
	select {
	case <-fast:
	default:
	}
	fast <- want
	r.lastFast, r.sentFast = want, true
}
