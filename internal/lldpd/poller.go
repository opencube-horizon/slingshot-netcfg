package lldpd

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/opencube-horizon/slingshot-netcfg/internal/config"
)

// Snapshot is the result of one successful poll. It is authoritative: it
// replaces the previous neighbor set completely.
type Snapshot struct {
	// Neighbors maps an interface name to its LLDP neighbors.
	Neighbors map[string][]Neighbor
	// Handled holds the interfaces lldpd handles (show interfaces).
	Handled map[string]bool
}

// Result is what the poller delivers after each poll: either a snapshot or
// the reason the poll failed.
type Result struct {
	Snapshot *Snapshot
	Err      error
}

// Poller polls lldpd by running lldpcli (§6.3).
type Poller struct {
	lldpcli    string
	neighbors  []string // argv of `show neighbors details`
	interfaces []string // argv of `show interfaces`
	timeout    time.Duration
	fast, slow time.Duration
}

// NewPoller resolves lldpcli and prepares both commands. Polls run every
// cfg.PollInterval while fast polling is wanted, otherwise every resync.
func NewPoller(cfg config.LLDPD, resync time.Duration) (*Poller, error) {
	path, err := exec.LookPath(cfg.LLDPCLI)
	if err != nil {
		return nil, fmt.Errorf("lldpcli: %w", err)
	}
	base := []string{"-f", "json0"}
	if cfg.Socket != "" {
		base = []string{"-u", cfg.Socket, "-f", "json0"}
	}
	return &Poller{
		lldpcli:    path,
		neighbors:  append(base[:len(base):len(base)], "show", "neighbors", "details"),
		interfaces: append(base[:len(base):len(base)], "show", "interfaces"),
		timeout:    cfg.CommandTimeout,
		fast:       cfg.PollInterval,
		slow:       resync,
	}, nil
}

// Path is the resolved lldpcli binary.
func (p *Poller) Path() string { return p.lldpcli }

// Poll runs one poll: `show neighbors details`, then `show interfaces`.
func (p *Poller) Poll(ctx context.Context) (*Snapshot, error) {
	out, err := p.run(ctx, p.neighbors)
	if err != nil {
		return nil, err
	}
	neighbors, err := DecodeNeighbors(out)
	if err != nil {
		return nil, fmt.Errorf("show neighbors: %w", err)
	}
	out, err = p.run(ctx, p.interfaces)
	if err != nil {
		return nil, err
	}
	names, err := DecodeInterfaces(out)
	if err != nil {
		return nil, fmt.Errorf("show interfaces: %w", err)
	}
	s := &Snapshot{Neighbors: map[string][]Neighbor{}, Handled: map[string]bool{}}
	for _, n := range neighbors {
		s.Neighbors[n.Interface] = append(s.Neighbors[n.Interface], n)
	}
	for _, name := range names {
		s.Handled[name] = true
	}
	return s, nil
}

// run executes lldpcli with a per-call timeout; stderr ends up in the error.
func (p *Poller) run(ctx context.Context, argv []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.lldpcli, argv...)
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	if ctx.Err() != nil {
		err = fmt.Errorf("%w after %s", ctx.Err(), p.timeout)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && len(ee.Stderr) > 0 {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(ee.Stderr)))
	}
	return nil, fmt.Errorf("lldpcli %s: %w", strings.Join(argv, " "), err)
}

// Run polls until ctx is cancelled and always returns nil then. Results are
// delivered on out, which keeps only the latest one: an unread older result
// is replaced. The fast interval applies while the last value received on
// fast is true, and after a failed poll.
func (p *Poller) Run(ctx context.Context, out chan Result, fast <-chan bool) error {
	wantFast := true
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case wantFast = <-fast:
			if wantFast {
				timer.Reset(p.fast)
			}
			continue
		case <-timer.C:
		}
		snap, err := p.Poll(ctx)
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-out:
		default:
		}
		out <- Result{Snapshot: snap, Err: err}
		next := p.slow
		if wantFast || err != nil {
			next = p.fast
		}
		timer.Reset(next)
	}
}
