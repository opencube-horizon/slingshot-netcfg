package reconcile

import (
	"bytes"
	"slices"

	"github.com/opencube-horizon/slingshot-netcfg/internal/craytlv"
	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
	"github.com/opencube-horizon/slingshot-netcfg/internal/discover"
	"github.com/opencube-horizon/slingshot-netcfg/internal/netapply"
)

type sysctlDev struct{ dev, owner string }

func sysctlDevs(in Input) []sysctlDev {
	out := make([]sysctlDev, 0, len(in.Ifaces)+len(in.VLANs))
	for _, i := range in.Ifaces {
		out = append(out, sysctlDev{i.Link.Name, i.Link.Name})
	}
	for _, v := range in.VLANs {
		out = append(out, sysctlDev{v.Link.Name, v.Parent})
	}
	return out
}

func setLink(i discover.Iface, t *desired.Target) (netapply.SetLink, bool) {
	op := netapply.SetLink{Name: i.Link.Name, Index: i.Link.Index}
	if !bytes.Equal(i.Link.MAC, t.MAC) {
		op.MAC = t.MAC
	}
	if i.Link.MTU != t.Payload.MTU {
		op.MTU = t.Payload.MTU
	}
	return op, op.MAC != nil || op.MTU != 0
}

// replaceAddr decides whether the target address must be added or its
// lifetime refreshed. A finite lifetime is refreshed once the remaining time
// drops below ttl − min(resync/2, ttl/2): every poll refreshes it, while the
// netlink event caused by the refresh itself doesn't trigger another one.
func replaceAddr(i discover.Iface, t *desired.Target, present []netapply.Addr, resync float64) (netapply.ReplaceAddr, bool) {
	op := netapply.ReplaceAddr{Name: i.Link.Name, Index: i.Link.Index, Prefix: t.Payload.Prefix, Lifetime: t.Payload.Lifetime}
	var have uint32
	found := false
	for _, a := range present {
		if a.Prefix == t.Payload.Prefix {
			have, found = a.ValidLft, true
			break
		}
	}
	if !found {
		return op, true
	}
	if t.Payload.Lifetime == craytlv.Forever {
		return op, have != uint32(craytlv.Forever)
	}
	ttl := float64(t.Payload.Lifetime)
	margin := min(resync/2, ttl/2)
	op.Refresh = true
	return op, have == uint32(craytlv.Forever) || float64(have) < ttl-margin
}

func moveLocalRule(rules []netapply.LocalRule) []netapply.Op {
	if len(rules) == 0 || slices.MinFunc(rules, byPriority).Priority >= 10 {
		return nil
	}
	var ops []netapply.Op
	if !slices.ContainsFunc(rules, func(r netapply.LocalRule) bool { return r.Priority == 10 }) {
		ops = append(ops, netapply.AddLocalRule{Priority: 10})
	}
	for _, r := range rules {
		if r.Priority < 10 {
			ops = append(ops, netapply.DelLocalRule{Rule: r})
		}
	}
	return ops
}

func byPriority(a, b netapply.LocalRule) int { return a.Priority - b.Priority }

// hsnAddrs returns the HSN addresses (§6.5) and the devices T of §6.7.
func hsnAddrs(in Input) ([]desired.Addr, []string) {
	var addrs []desired.Addr
	var devices []string
	for _, i := range in.Ifaces {
		devices = append(devices, i.Link.Name)
		if t := in.Views[i.Link.Name].Target; t != nil {
			addrs = append(addrs, desired.Addr{Prefix: t.Payload.Prefix, Dev: i.Link.Name, N: i.N})
			continue
		}
		for _, a := range in.State.Addrs[i.Link.Name] {
			addrs = append(addrs, desired.Addr{Prefix: a.Prefix, Dev: i.Link.Name, N: i.N})
		}
	}
	if !in.Cfg.Routing.VLANs {
		return addrs, devices
	}
	for _, v := range in.VLANs {
		devices = append(devices, v.Link.Name)
		for _, a := range in.State.Addrs[v.Link.Name] {
			addrs = append(addrs, desired.Addr{Prefix: a.Prefix, Dev: v.Link.Name, N: v.N})
		}
	}
	return addrs, devices
}

func ownerByDev(in Input) map[string]string {
	m := map[string]string{}
	for _, i := range in.Ifaces {
		m[i.Link.Name] = i.Link.Name
	}
	for _, v := range in.VLANs {
		m[v.Link.Name] = v.Parent
	}
	return m
}
