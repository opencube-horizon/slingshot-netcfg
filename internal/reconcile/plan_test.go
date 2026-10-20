package reconcile

import (
	"net"
	"net/netip"
	"slices"
	"testing"

	"github.com/opencube-horizon/slingshot-netcfg/internal/config"
	"github.com/opencube-horizon/slingshot-netcfg/internal/craytlv"
	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
	"github.com/opencube-horizon/slingshot-netcfg/internal/discover"
	"github.com/opencube-horizon/slingshot-netcfg/internal/netapply"
)

var (
	cfg     = config.Default()
	ama0    = net.HardwareAddr{0x02, 0x00, 0x00, 0x00, 0x00, 0x37}
	addr0   = netip.MustParsePrefix("172.24.0.7/16")
	addr1   = netip.MustParsePrefix("172.24.0.8/16")
	hsn0    = discover.Iface{Link: netapply.Link{Index: 10, Name: "hsn0", MAC: net.HardwareAddr{0, 0x40, 0xa6, 0x95, 0x13, 0xa8}, MTU: 1500, Running: true}, N: 0}
	forever = uint32(craytlv.Forever)
)

func target(prefix netip.Prefix, lifetime craytlv.Lifetime) *desired.Target {
	return &desired.Target{MAC: ama0, Payload: craytlv.Payload{Prefix: prefix, MTU: 9000, Lifetime: lifetime}}
}

func localRules(prios ...int) []netapply.LocalRule {
	out := make([]netapply.LocalRule, 0, len(prios))
	for _, p := range prios {
		out = append(out, netapply.LocalRule{Priority: p, Protocol: 2})
	}
	return out
}

func emptyState() netapply.State {
	return netapply.State{Addrs: map[string][]netapply.Addr{}, Sysctls: map[string]string{}, LocalRules: localRules(0, 32766, 32767)}
}

// converged returns the kernel state after a successful first provisioning of hsn0.
func converged(prefix netip.Prefix) (discover.Iface, netapply.State) {
	i := hsn0
	i.Link.MAC, i.Link.MTU, i.Link.Up = ama0, 9000, true
	s := emptyState()
	s.LocalRules = localRules(10, 32766, 32767)
	for _, sc := range netapply.InterfaceSysctls("hsn0", true) {
		s.Sysctls[sc.Path] = sc.Value
	}
	s.Addrs["hsn0"] = []netapply.Addr{{Prefix: prefix, ValidLft: forever}}
	a := []desired.Addr{{Prefix: prefix, Dev: "hsn0"}}
	s.Rules = desired.Rules(a, []string{"hsn0"}, 200)
	s.Routes = desired.Routes(a, 200)
	return i, s
}

func kinds(ops []netapply.Op) []string {
	var out []string
	for _, o := range ops {
		var k string
		switch o.(type) {
		case netapply.SetSysctl:
			k = "sysctl"
		case netapply.SetLink:
			k = "link"
		case netapply.LinkUp:
			k = "up"
		case netapply.AddLocalRule:
			k = "+local"
		case netapply.DelLocalRule:
			k = "-local"
		case netapply.AddRule:
			k = "+rule"
		case netapply.DelRule:
			k = "-rule"
		case netapply.ReplaceAddr:
			k = "addr"
		case netapply.DelAddr:
			k = "-addr"
		case netapply.ReplaceRoute:
			k = "route"
		case netapply.DelRoute:
			k = "-route"
		case netapply.FlushNeighbors:
			k = "flush"
		}
		if len(out) == 0 || out[len(out)-1] != k {
			out = append(out, k)
		}
	}
	return out
}

func TestPlan_firstProvisioning_followsSpecOrder(t *testing.T) {
	in := Input{
		Cfg: cfg, State: emptyState(), Ifaces: []discover.Iface{hsn0},
		Views: map[string]View{"hsn0": {Known: true, Neighbors: 1, Target: target(addr0, craytlv.Forever)}},
	}

	got := kinds(Plan(in))

	want := []string{"sysctl", "link", "up", "+local", "-local", "+rule", "addr", "route", "flush"}
	if !slices.Equal(got, want) {
		t.Errorf("op order = %v, want %v", got, want)
	}
}

func TestPlan_isEmpty_whenConverged(t *testing.T) {
	i, s := converged(addr0)
	in := Input{
		Cfg: cfg, State: s, Ifaces: []discover.Iface{i},
		Views: map[string]View{"hsn0": {Known: true, Neighbors: 1, Target: target(addr0, craytlv.Forever)}},
	}

	if ops := Plan(in); len(ops) != 0 {
		t.Errorf("ops = %v, want none", ops)
	}
}

func TestPlan_replacesAddressRulesAndRoutes_whenTargetChanges(t *testing.T) {
	i, s := converged(addr0)
	in := Input{
		Cfg: cfg, State: s, Ifaces: []discover.Iface{i},
		Views: map[string]View{"hsn0": {Known: true, Neighbors: 1, Target: target(addr1, craytlv.Forever)}},
	}

	got := kinds(Plan(in))

	want := []string{"+rule", "addr", "-addr", "route", "-rule", "-route", "flush"}
	if !slices.Equal(got, want) {
		t.Errorf("op order = %v, want %v", got, want)
	}
}

func TestPlan_keepsRouting_whenNeighborUnknownOrGone(t *testing.T) {
	for name, view := range map[string]View{"unknown": {}, "stale": {Known: true}} {
		t.Run(name, func(t *testing.T) {
			i, s := converged(addr0)
			in := Input{Cfg: cfg, State: s, Ifaces: []discover.Iface{i}, Views: map[string]View{"hsn0": view}}
			if ops := Plan(in); len(ops) != 0 {
				t.Errorf("ops = %v, want none", ops)
			}
		})
	}
}

func TestPlan_removesRouting_whenAddressExpired(t *testing.T) {
	i, s := converged(addr0)
	s.Addrs["hsn0"] = nil
	in := Input{Cfg: cfg, State: s, Ifaces: []discover.Iface{i}, Views: map[string]View{"hsn0": {Known: true}}}

	got := kinds(Plan(in))

	if want := []string{"-rule", "-route"}; !slices.Equal(got, want) {
		t.Errorf("ops = %v, want %v", got, want)
	}
}

func TestPlan_neverDeletesLocalRule_atOrAbove10(t *testing.T) {
	i, s := converged(addr0)
	s.LocalRules = localRules(100)
	in := Input{
		Cfg: cfg, State: s, Ifaces: []discover.Iface{i},
		Views: map[string]View{"hsn0": {Known: true, Target: target(addr0, craytlv.Forever)}},
	}

	if ops := Plan(in); len(ops) != 0 {
		t.Errorf("ops = %v, want none (CNI moved local rule already)", ops)
	}
}

func TestPlan_refreshesFiniteLifetime_onlyBelowThreshold(t *testing.T) {
	cases := map[string]struct {
		remaining uint32
		want      bool
	}{
		"just refreshed":     {3600, false},
		"within margin":      {3600 - 29, false},
		"one resync later":   {3600 - 60, true},
		"previously forever": {forever, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			i, s := converged(addr0)
			s.Addrs["hsn0"][0].ValidLft = tc.remaining
			in := Input{
				Cfg: cfg, State: s, Ifaces: []discover.Iface{i},
				Views: map[string]View{"hsn0": {Known: true, Target: target(addr0, 3600)}},
			}

			ops := Plan(in)

			refresh := len(ops) == 1 && ops[0].(netapply.ReplaceAddr).Refresh
			if refresh != tc.want {
				t.Errorf("ops = %v, want refresh=%v", ops, tc.want)
			}
		})
	}
}

func TestPlan_flushesNeighbors_whenAMAChanges(t *testing.T) {
	i, s := converged(addr0)
	i.Link.MAC = net.HardwareAddr{0x02, 0x00, 0, 0, 0, 0x99}
	in := Input{
		Cfg: cfg, State: s, Ifaces: []discover.Iface{i},
		Views: map[string]View{"hsn0": {Known: true, Target: target(addr0, craytlv.Forever)}},
	}

	if got := kinds(Plan(in)); !slices.Equal(got, []string{"link", "flush"}) {
		t.Errorf("ops = %v, want link then flush", got)
	}
}

func TestPlan_routesVLANAddress_withParentTable(t *testing.T) {
	i, s := converged(addr0)
	vlan := discover.VLAN{Link: netapply.Link{Index: 20, Name: "hsn0.100", Kind: "vlan", ParentIndex: 10}, N: 0, Parent: "hsn0"}
	vaddr := netip.MustParsePrefix("192.168.100.5/24")
	s.Addrs["hsn0.100"] = []netapply.Addr{{Prefix: vaddr, ValidLft: forever}}
	in := Input{
		Cfg: cfg, State: s, Ifaces: []discover.Iface{i}, VLANs: []discover.VLAN{vlan},
		Views: map[string]View{"hsn0": {Known: true, Target: target(addr0, craytlv.Forever)}},
	}

	ops := Plan(in)

	var route *desired.Route
	var iifRule, flushed bool
	for _, o := range ops {
		switch o := o.(type) {
		case netapply.ReplaceRoute:
			route = &o.Route
		case netapply.AddRule:
			iifRule = iifRule || (o.Rule.IIF == "hsn0.100" && o.Rule.Src.Addr() == addr0.Addr())
		case netapply.FlushNeighbors:
			flushed = flushed || o.Dev == "hsn0.100"
		default:
		}
	}
	if route == nil || route.Table != 200 || route.Dev != "hsn0.100" || route.Dst != vaddr.Masked() {
		t.Errorf("VLAN route = %+v", route)
	}
	if !iifRule || !flushed {
		t.Errorf("iif rule for hsn0 address = %v, VLAN neighbor flush = %v; ops %v", iifRule, flushed, ops)
	}
}

func TestPlan_skipsVLANFlush_whenAddressSeenBefore(t *testing.T) {
	i, s := converged(addr0)
	vlan := discover.VLAN{Link: netapply.Link{Index: 20, Name: "hsn0.100"}, N: 0, Parent: "hsn0"}
	vaddr := netip.MustParsePrefix("192.168.100.5/24")
	s.Addrs["hsn0.100"] = []netapply.Addr{{Prefix: vaddr}}
	in := Input{
		Cfg: cfg, State: s, Ifaces: []discover.Iface{i}, VLANs: []discover.VLAN{vlan},
		SeenVLANAddrs: map[netip.Prefix]bool{vaddr: true},
		Views:         map[string]View{"hsn0": {Known: true, Target: target(addr0, craytlv.Forever)}},
	}

	for _, o := range Plan(in) {
		if _, ok := o.(netapply.FlushNeighbors); ok {
			t.Errorf("unexpected flush %v", o)
		}
	}
}

func TestPlan_removesAllTaggedObjects_whenRoutingDisabled(t *testing.T) {
	i, s := converged(addr0)
	c := cfg
	c.Routing.Enabled = false
	in := Input{
		Cfg: c, State: s, Ifaces: []discover.Iface{i},
		Views: map[string]View{"hsn0": {Known: true, Target: target(addr0, craytlv.Forever)}},
	}

	if got := kinds(Plan(in)); !slices.Equal(got, []string{"-rule", "-route"}) {
		t.Errorf("ops = %v", got)
	}
}
