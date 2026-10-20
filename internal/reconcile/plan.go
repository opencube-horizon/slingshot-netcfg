// Package reconcile runs the slingshot-netcfg passes: it plans the kernel
// changes from observed state and lldpd neighbor information (spec §6.6),
// executes them, and tracks per-interface phases, backoff and contention.
package reconcile

import (
	"bytes"
	"net/netip"
	"slices"

	"github.com/opencube-horizon/slingshot-netcfg/internal/config"
	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
	"github.com/opencube-horizon/slingshot-netcfg/internal/discover"
	"github.com/opencube-horizon/slingshot-netcfg/internal/netapply"
)

// View is what is known about an HSN interface's LLDP neighbor.
type View struct {
	// Known is false until the first successful lldpd poll.
	Known bool
	// Neighbors counts the LLDP neighbors in the last snapshot.
	Neighbors int
	Target    *desired.Target
	Err       error
}

// Input is everything one pass plans from.
type Input struct {
	Cfg    config.Config
	State  netapply.State
	Ifaces []discover.Iface
	VLANs  []discover.VLAN
	Views  map[string]View
	// SeenVLANAddrs are VLAN addresses already present in an earlier pass;
	// neighbor cleanup only runs for new ones.
	SeenVLANAddrs map[netip.Prefix]bool
}

// Plan returns the operations of one pass in the order of §6.6.
func Plan(in Input) []netapply.Op {
	want := wantRouting(in)
	addrOps, flushes := planAddrs(in)

	var ops []netapply.Op
	ops = append(ops, planSysctls(in)...)
	ops = append(ops, planLinkSettings(in)...)
	ops = append(ops, planLinksUp(in)...)
	if in.Cfg.Routing.Enabled && in.Cfg.Routing.MoveLocalRule {
		ops = append(ops, moveLocalRule(in.State.LocalRules)...)
	}
	ops = append(ops, planAddRules(want.rules, in.State.Rules)...)
	ops = append(ops, addrOps...)
	ops = append(ops, planAddRoutes(want.routes, in)...)
	ops = append(ops, planCleanup(want, in.State)...)
	ops = append(ops, flushes...)
	return append(ops, planVLANFlushes(in)...)
}

// routing is the desired policy routing of a pass.
type routing struct {
	rules  []desired.Rule
	routes []desired.Route
}

func wantRouting(in Input) routing {
	if !in.Cfg.Routing.Enabled {
		return routing{}
	}
	addrs, devices := hsnAddrs(in)
	return routing{
		rules:  desired.Rules(addrs, devices, in.Cfg.Routing.TableBase),
		routes: desired.Routes(addrs, in.Cfg.Routing.TableBase),
	}
}

func planSysctls(in Input) []netapply.Op {
	var ops []netapply.Op
	for _, d := range sysctlDevs(in) {
		for _, s := range netapply.InterfaceSysctls(d.dev, in.Cfg.IPv6.Disable) {
			if in.State.Sysctls[s.Path] != s.Value {
				ops = append(ops, netapply.SetSysctl{Dev: d.dev, Owner: d.owner, Sysctl: s})
			}
		}
	}
	return ops
}

func planLinkSettings(in Input) []netapply.Op {
	var ops []netapply.Op
	for _, i := range in.Ifaces {
		if t := in.Views[i.Link.Name].Target; t != nil {
			if op, ok := setLink(i, t); ok {
				ops = append(ops, op)
			}
		}
	}
	return ops
}

func planLinksUp(in Input) []netapply.Op {
	var ops []netapply.Op
	for _, i := range in.Ifaces {
		if !i.Link.Up {
			ops = append(ops, netapply.LinkUp{Name: i.Link.Name, Index: i.Link.Index})
		}
	}
	return ops
}

func planAddRules(want, have []desired.Rule) []netapply.Op {
	var ops []netapply.Op
	for _, r := range want {
		if !slices.Contains(have, r) {
			ops = append(ops, netapply.AddRule{Rule: r})
		}
	}
	return ops
}

// planAddrs returns the address operations (target address, then foreign
// addresses per interface) and the neighbor flushes they require.
func planAddrs(in Input) (ops, flushes []netapply.Op) {
	for _, i := range in.Ifaces {
		t := in.Views[i.Link.Name].Target
		if t == nil {
			continue
		}
		present := in.State.Addrs[i.Link.Name]
		op, needed := replaceAddr(i, t, present, in.Cfg.ResyncInterval.Seconds())
		if needed {
			ops = append(ops, op)
		}
		if (needed && !op.Refresh) || !bytes.Equal(i.Link.MAC, t.MAC) {
			flushes = append(flushes, netapply.FlushNeighbors{Owner: i.Link.Name, Dev: i.Link.Name, Prefix: t.Payload.Prefix.Masked()})
		}
		if in.Cfg.Apply.FlushForeignAddrs {
			ops = append(ops, delForeignAddrs(i, t, present)...)
		}
	}
	return ops, flushes
}

func delForeignAddrs(i discover.Iface, t *desired.Target, present []netapply.Addr) []netapply.Op {
	var ops []netapply.Op
	for _, a := range present {
		if a.Prefix != t.Payload.Prefix {
			ops = append(ops, netapply.DelAddr{Name: i.Link.Name, Index: i.Link.Index, Prefix: a.Prefix})
		}
	}
	return ops
}

func planAddRoutes(want []desired.Route, in Input) []netapply.Op {
	owner := ownerByDev(in)
	var ops []netapply.Op
	for _, r := range want {
		if !slices.Contains(in.State.Routes, r) {
			ops = append(ops, netapply.ReplaceRoute{Owner: owner[r.Dev], Route: r})
		}
	}
	return ops
}

// planCleanup deletes tagged rules and routes that aren't desired (§6.5).
func planCleanup(want routing, st netapply.State) []netapply.Op {
	var ops []netapply.Op
	for _, r := range st.Rules {
		if !slices.Contains(want.rules, r) {
			ops = append(ops, netapply.DelRule{Rule: r})
		}
	}
	for _, r := range st.Routes {
		if !slices.Contains(want.routes, r) {
			ops = append(ops, netapply.DelRoute{Route: r})
		}
	}
	return ops
}

func planVLANFlushes(in Input) []netapply.Op {
	if !in.Cfg.Routing.VLANs {
		return nil
	}
	var ops []netapply.Op
	for _, v := range in.VLANs {
		for _, a := range in.State.Addrs[v.Link.Name] {
			if !in.SeenVLANAddrs[a.Prefix] {
				ops = append(ops, netapply.FlushNeighbors{Owner: v.Parent, Dev: v.Link.Name, Prefix: a.Prefix.Masked()})
			}
		}
	}
	return ops
}
