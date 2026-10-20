//go:build integration

package integration

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
)

func TestProvisioning(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root and network namespaces (run scripts/integration-test.sh)")
	}
	p0 := newPeer(0, "02:fe:00:00:00:37")
	p1 := newPeer(1, "02:fe:00:00:00:38")
	p0.create(t)
	p1.create(t)
	p0.advertise(t, payload("172.24.0.7/16", `"forever"`), false)
	p1.advertise(t, payload("172.24.0.8/16", "20"), false)
	host := startLLDPD(t, nil, hostSocket, "host", "hsn*")
	t.Cleanup(func() { _ = host.Process.Kill(); _ = host.Wait() })

	runDir := t.TempDir()
	sysRoot := cassiniSysfs(t, "hsn0", "hsn1")
	s := startService(t, runDir, sysRoot)
	defer func() { s.stop(t) }()

	t.Run("provisions both interfaces", func(t *testing.T) {
		eventually(t, 30*time.Second, func() error {
			for ifname, want := range map[string][2]string{
				"hsn0": {"02:00:00:00:00:37", "172.24.0.7/16"},
				"hsn1": {"02:00:00:00:00:38", "172.24.0.8/16"},
			} {
				l, err := netlink.LinkByName(ifname)
				if err != nil {
					return err
				}
				if l.Attrs().HardwareAddr.String() != want[0] || l.Attrs().MTU != 9000 || !hasAddr(t, ifname, want[1]) {
					return fmt.Errorf("%s: mac %s mtu %d addrs %v", ifname, l.Attrs().HardwareAddr, l.Attrs().MTU, addrs(t, ifname, netlink.FAMILY_V4))
				}
				if _, err := os.Stat(filepath.Join(runDir, ifname+".ready")); err != nil {
					return err
				}
			}
			return nil
		})
		if lft := addrs(t, "hsn1", netlink.FAMILY_V4)[0].ValidLft; lft > 20 {
			t.Errorf("hsn1 valid lifetime %d, want ≤ 20 from the TLV", lft)
		}
	})

	t.Run("sysctls written and no IPv6 on HSN interfaces", func(t *testing.T) {
		for path, want := range map[string]string{
			"ipv4/conf/hsn0/rp_filter": "2", "ipv4/conf/hsn0/arp_ignore": "1", "ipv4/conf/hsn0/arp_announce": "2",
			"ipv4/conf/hsn0/arp_notify": "1", "ipv4/conf/hsn0/accept_local": "1", "ipv6/conf/hsn0/disable_ipv6": "1",
		} {
			b, _ := os.ReadFile("/proc/sys/net/" + path)
			if got := strings.TrimSpace(string(b)); got != want {
				t.Errorf("%s = %q, want %q", path, got, want)
			}
		}
		if a := addrs(t, "hsn0", netlink.FAMILY_V6); len(a) != 0 {
			t.Errorf("hsn0 has IPv6 addresses %v", a)
		}
	})

	t.Run("tagged rules and table routes match spec", func(t *testing.T) {
		want := desired.Rules([]desired.Addr{
			{Prefix: netip.MustParsePrefix("172.24.0.7/16"), Dev: "hsn0", N: 0},
			{Prefix: netip.MustParsePrefix("172.24.0.8/16"), Dev: "hsn1", N: 1},
		}, []string{"hsn0", "hsn1"}, 200)
		if got := taggedRules(t); !sameSet(got, want) {
			t.Errorf("tagged rules\n got %v\nwant %v", got, want)
		}
		for table, dev := range map[int]string{200: "hsn0", 201: "hsn1"} {
			routes, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: table}, netlink.RT_FILTER_TABLE)
			if err != nil || len(routes) != 1 || routes[0].Protocol != protocol || routes[0].Scope != netlink.SCOPE_HOST ||
				routes[0].Dst.String() != "172.24.0.0/16" {
				t.Errorf("table %d (%s) routes = %v, %v", table, dev, routes, err)
			}
		}
		rules, _ := netlink.RuleList(netlink.FAMILY_V4)
		var localPrefs []int
		for _, r := range rules {
			if r.Table == unix.RT_TABLE_LOCAL && r.Src == nil && r.Dst == nil && r.IifName == "" {
				localPrefs = append(localPrefs, r.Priority)
			}
		}
		if !slices.Equal(localPrefs, []int{10}) {
			t.Errorf("selector-less local rules at %v, want only pref 10", localPrefs)
		}
	})

	t.Run("cleanup removes stray tagged rules only", func(t *testing.T) {
		stray := netlink.NewRule()
		stray.Priority, stray.Table, stray.Protocol = 3, 300, protocol
		stray.Src = &net.IPNet{IP: net.IPv4(1, 2, 3, 4), Mask: net.CIDRMask(32, 32)}
		foreign := netlink.NewRule()
		foreign.Priority, foreign.Table = 4, 300
		foreign.Src = &net.IPNet{IP: net.IPv4(1, 2, 3, 5), Mask: net.CIDRMask(32, 32)}
		for _, r := range []*netlink.Rule{stray, foreign} {
			if err := netlink.RuleAdd(r); err != nil {
				t.Fatal(err)
			}
		}
		defer netlink.RuleDel(foreign)
		eventually(t, 15*time.Second, func() error {
			if slices.ContainsFunc(taggedRules(t), func(r desired.Rule) bool { return r.Table == 300 }) {
				return errNotYet
			}
			return nil
		})
		rules, _ := netlink.RuleList(netlink.FAMILY_V4)
		if !slices.ContainsFunc(rules, func(r netlink.Rule) bool { return r.Priority == 4 && r.Table == 300 }) {
			t.Error("untagged foreign rule was removed")
		}
	})

	t.Run("VLAN address gets routing in the parent table", func(t *testing.T) {
		parent, _ := netlink.LinkByName("hsn0")
		vlan := &netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "hsn0.100", ParentIndex: parent.Attrs().Index}, VlanId: 100}
		if err := netlink.LinkAdd(vlan); err != nil {
			t.Fatal(err)
		}
		defer netlink.LinkDel(vlan)
		vaddr, _ := netlink.ParseAddr("192.168.100.5/24")
		if err := netlink.AddrAdd(vlan, vaddr); err != nil {
			t.Fatal(err)
		}
		_ = netlink.LinkSetUp(vlan)
		vlanRouted := func() bool {
			routes, _ := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: 200}, netlink.RT_FILTER_TABLE)
			return slices.ContainsFunc(routes, func(r netlink.Route) bool { return r.Dst.String() == "192.168.100.0/24" })
		}
		eventually(t, 15*time.Second, func() error {
			iif := slices.ContainsFunc(taggedRules(t), func(r desired.Rule) bool {
				return r.IIF == "hsn0.100" && r.Src == netip.MustParsePrefix("172.24.0.7/32")
			})
			if !vlanRouted() || !iif {
				return errNotYet
			}
			return nil
		})
		if err := netlink.AddrDel(vlan, vaddr); err != nil {
			t.Fatal(err)
		}
		eventually(t, 15*time.Second, func() error {
			if vlanRouted() {
				return errNotYet
			}
			return nil
		})
	})

	t.Run("TLV change replaces address and routing", func(t *testing.T) {
		p0.advertise(t, payload("172.24.0.17/16", `"forever"`), true)
		eventually(t, 30*time.Second, func() error {
			old := slices.ContainsFunc(taggedRules(t), func(r desired.Rule) bool { return r.Src == netip.MustParsePrefix("172.24.0.7/32") })
			if hasAddr(t, "hsn0", "172.24.0.7/16") || !hasAddr(t, "hsn0", "172.24.0.17/16") || old {
				return errNotYet
			}
			return nil
		})
	})

	t.Run("restart and lldpd restart cause no link flap", func(t *testing.T) {
		updates := make(chan netlink.LinkUpdate, 64)
		done := make(chan struct{})
		if err := netlink.LinkSubscribe(updates, done); err != nil {
			t.Fatal(err)
		}
		defer close(done)
		s.stop(t)
		_ = host.Process.Kill()
		_ = host.Wait()
		time.Sleep(3 * time.Second)
		host = startLLDPD(t, nil, hostSocket, "host2", "hsn*")
		s = startService(t, runDir, sysRoot)
		time.Sleep(8 * time.Second)
		for len(updates) > 0 {
			u := <-updates
			if strings.HasPrefix(u.Attrs().Name, "hsn") && u.Flags&unix.IFF_UP == 0 {
				t.Errorf("%s went down during restart", u.Attrs().Name)
			}
		}
		if !hasAddr(t, "hsn0", "172.24.0.17/16") || !hasAddr(t, "hsn1", "172.24.0.8/16") {
			t.Error("addresses changed across restart")
		}
	})

	t.Run("contention is reported", func(t *testing.T) {
		l, _ := netlink.LinkByName("hsn0")
		for range 3 {
			if err := netlink.LinkSetMTU(l, 1500); err != nil {
				t.Fatal(err)
			}
			eventually(t, 40*time.Second, func() error {
				l2, _ := netlink.LinkByName("hsn0")
				if l2.Attrs().MTU != 9000 {
					return errNotYet
				}
				return nil
			})
		}
		eventually(t, 10*time.Second, func() error {
			if phase(runDir, "hsn0") != "error" {
				return errNotYet
			}
			return nil
		})
	})

	t.Run("neighbor loss: stale, then finite lifetime expires", func(t *testing.T) {
		p1.stop()
		eventually(t, 15*time.Second, func() error {
			if phase(runDir, "hsn1") != "stale" {
				return fmt.Errorf("phase %q", phase(runDir, "hsn1"))
			}
			return nil
		})
		eventually(t, 40*time.Second, func() error {
			old := slices.ContainsFunc(taggedRules(t), func(r desired.Rule) bool { return r.Src == netip.MustParsePrefix("172.24.0.8/32") })
			if hasAddr(t, "hsn1", "172.24.0.8/16") || old || phase(runDir, "hsn1") != "waiting-neighbor" {
				return errNotYet
			}
			return nil
		})
	})
}
