package reconcile

import (
	"errors"
	"net/netip"
	"time"

	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
	"github.com/opencube-horizon/slingshot-netcfg/internal/discover"
	"github.com/opencube-horizon/slingshot-netcfg/internal/netapply"
)

// pass runs one reconcile pass and reports whether every selected interface
// is provisioned (and at least one is selected).
func (r *Reconciler) pass(now time.Time) bool {
	links, err := r.Kernel.ObserveLinks()
	if err != nil {
		r.Log.Error("observe links", "err", err)
		return false
	}
	ifaces, vlans := r.Selector.Select(links)
	devs := make([]string, 0, len(ifaces)+len(vlans))
	for _, i := range ifaces {
		devs = append(devs, i.Link.Name)
	}
	for _, v := range vlans {
		devs = append(devs, v.Link.Name)
	}
	st, err := r.Kernel.Observe(links, devs, r.Cfg.IPv6.Disable)
	if err != nil {
		r.Log.Error("observe kernel state", "err", err)
		return false
	}

	r.trackSelection(ifaces)
	views := make(map[string]View, len(ifaces))
	for _, i := range ifaces {
		s := r.ifs[i.Link.Name]
		s.pci, s.running = i.PCI, i.Link.Running
		views[i.Link.Name] = r.view(i.Link.Name, s, now)
	}

	ops := Plan(Input{Cfg: r.Cfg, State: st, Ifaces: ifaces, VLANs: vlans, Views: views, SeenVLANAddrs: r.seenVLAN})
	pending := r.execute(ops, now)
	if r.DryRun {
		r.Log.Info("dry run: nothing applied", "operations", len(ops))
		return false
	}

	r.seenVLAN = map[netip.Prefix]bool{}
	for _, v := range vlans {
		for _, a := range st.Addrs[v.Link.Name] {
			r.seenVLAN[a.Prefix] = true
		}
	}
	allProvisioned := len(ifaces) > 0
	for _, i := range ifaces {
		s := r.ifs[i.Link.Name]
		phase := phaseOf(s, views[i.Link.Name], len(st.Addrs[i.Link.Name]) > 0, pending[i.Link.Name])
		if phase != s.phase {
			r.Log.Info("phase changed", "ifname", i.Link.Name, "from", s.phase, "to", phase, "err", errText(firstErr(views[i.Link.Name].Err, s.lastErr)))
			s.phase = phase
		}
		if phase == PhaseProvisioned {
			s.markConverged()
		}
		allProvisioned = allProvisioned && phase == PhaseProvisioned
		r.writeReady(i, s)
	}
	r.writeState(ifaces, st)
	return allProvisioned
}

func (r *Reconciler) trackSelection(ifaces []discover.Iface) {
	seen := map[string]bool{}
	for _, i := range ifaces {
		seen[i.Link.Name] = true
		if _, ok := r.ifs[i.Link.Name]; !ok {
			r.Log.Info("interface selected", "ifname", i.Link.Name, "pci", i.PCI, "n", i.N)
			r.ifs[i.Link.Name] = &ifaceState{}
		}
	}
	for name := range r.ifs {
		if !seen[name] {
			r.Log.Info("interface deselected", "ifname", name)
			delete(r.ifs, name)
		}
	}
}

// view derives the interface's neighbor view from the latest snapshot and
// logs neighbor and target changes.
func (r *Reconciler) view(name string, s *ifaceState, now time.Time) View {
	if r.snapshot == nil {
		return View{}
	}
	neighbors := r.snapshot.Neighbors[name]
	v := View{Known: true, Neighbors: len(neighbors)}
	if s.running && !r.snapshot.Handled[name] {
		if s.unhandledSince.IsZero() {
			s.unhandledSince = now
		} else if now.Sub(s.unhandledSince) > unhandledAfter && v.Neighbors == 0 {
			v.Err = errNotHandled
		}
	} else {
		s.unhandledSince = time.Time{}
	}
	if len(neighbors) > 0 {
		t, err := desired.DeriveTarget(neighbors, r.Cfg.OrgTLV, r.Cfg.MAC)
		if err != nil {
			v.Err = err
		} else {
			v.Target = &t
		}
	}
	s.viewErr = v.Err
	r.noteNeighbor(name, s, v, now)
	return v
}

var errNotHandled = errors.New("interface not handled by lldpd")

// execute runs ops in order and returns the interfaces whose operations
// didn't all succeed (failed, or skipped because of backoff/contention).
func (r *Reconciler) execute(ops []netapply.Op, now time.Time) map[string]bool {
	pending := map[string]bool{}
	attempted := map[string]bool{}
	for _, s := range r.ifs {
		s.pruneExternal(now)
	}
	for _, op := range ops {
		name := op.Iface()
		s := r.ifs[name]
		if s != nil && (pending[name] || s.blocked(now)) {
			pending[name] = true
			continue
		}
		if r.DryRun {
			r.Log.Info("planned", "op", op.String())
			continue
		}
		if s != nil {
			attempted[name] = true
			s.observeExternal(op, now)
		}
		if err := r.Kernel.Apply(op); err != nil {
			r.Log.Error("apply failed", "op", op.String(), "ifname", name, "err", err)
			if s != nil {
				s.failed(now, err)
				pending[name] = true
			}
			continue
		}
		r.logApplied(op)
		if s != nil {
			s.recordApplied(op)
		}
	}
	for name, s := range r.ifs {
		if pending[name] {
			continue
		}
		s.lastErr = nil
		if !attempted[name] {
			continue
		}
		s.succeeded()
		if s.contended {
			r.Log.Warn("another agent is modifying the interface", "ifname", name, "hint", hintContention)
			s.notBefore = now.Add(contentionBackoff)
		}
	}
	return pending
}

func (r *Reconciler) logApplied(op netapply.Op) {
	if a, ok := op.(netapply.ReplaceAddr); ok && a.Refresh {
		r.Log.Debug("applied", "op", op.String())
		return
	}
	r.Log.Info("applied", "op", op.String())
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
