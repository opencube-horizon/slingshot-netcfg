package netapply

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
)

// ObserveLinks lists all network interfaces.
func (k *Kernel) ObserveLinks() ([]Link, error) {
	links, err := k.Handle.LinkList()
	if err != nil {
		return nil, fmt.Errorf("list links: %w", err)
	}
	out := make([]Link, 0, len(links))
	for _, l := range links {
		a := l.Attrs()
		kind := l.Type()
		if kind == "device" {
			kind = ""
		}
		out = append(out, Link{
			Index:       a.Index,
			Name:        a.Name,
			Kind:        kind,
			ParentIndex: a.ParentIndex,
			MAC:         a.HardwareAddr,
			MTU:         a.MTU,
			Up:          a.Flags&net.FlagUp != 0,
			Running:     a.RawFlags&unix.IFF_RUNNING != 0,
		})
	}
	return out, nil
}

// Observe reads addresses, owned rules and routes, local rules, and the
// §6.8 sysctls of devs.
func (k *Kernel) Observe(links []Link, devs []string, disableIPv6 bool) (State, error) {
	names := make(map[int]string, len(links))
	for _, l := range links {
		names[l.Index] = l.Name
	}
	s := State{Links: links}
	var err error
	if s.Addrs, err = k.observeAddrs(names); err != nil {
		return State{}, err
	}
	if s.Rules, s.LocalRules, err = k.observeRules(); err != nil {
		return State{}, err
	}
	if s.Routes, err = k.observeRoutes(names); err != nil {
		return State{}, err
	}
	if s.Sysctls, err = k.observeSysctls(devs, disableIPv6); err != nil {
		return State{}, err
	}
	return s, nil
}

func (k *Kernel) observeAddrs(names map[int]string) (map[string][]Addr, error) {
	addrs, err := k.Handle.AddrList(nil, netlink.FAMILY_V4)
	if err != nil {
		return nil, fmt.Errorf("list addresses: %w", err)
	}
	out := map[string][]Addr{}
	for _, a := range addrs {
		p, ok := prefixOf(a.IPNet)
		if !ok || a.Scope != unix.RT_SCOPE_UNIVERSE {
			continue
		}
		name := names[a.LinkIndex]
		out[name] = append(out[name], Addr{Prefix: p, ValidLft: uint32(a.ValidLft)}) //nolint:gosec // G115: the kernel reports valid_lft as u32.
	}
	return out, nil
}

// observeRules returns the tagged rules and the selector-less local rules.
func (k *Kernel) observeRules() ([]desired.Rule, []LocalRule, error) {
	rules, err := k.Handle.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return nil, nil, fmt.Errorf("list rules: %w", err)
	}
	var owned []desired.Rule
	var local []LocalRule
	for _, r := range rules {
		switch {
		case r.Protocol == k.Protocol:
			owned = append(owned, ruleOf(r))
		case r.Table == desired.TableLocal && selectorless(r):
			local = append(local, LocalRule{Priority: r.Priority, Protocol: r.Protocol})
		}
	}
	return owned, local, nil
}

func (k *Kernel) observeRoutes(names map[int]string) ([]desired.Route, error) {
	filter := &netlink.Route{Protocol: netlink.RouteProtocol(k.Protocol), Table: unix.RT_TABLE_UNSPEC}
	routes, err := k.Handle.RouteListFiltered(netlink.FAMILY_V4, filter, netlink.RT_FILTER_PROTOCOL|netlink.RT_FILTER_TABLE)
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}
	out := make([]desired.Route, 0, len(routes))
	for _, r := range routes {
		dst, _ := prefixOf(r.Dst)
		src, _ := netip.AddrFromSlice(r.Src)
		out = append(out, desired.Route{Table: r.Table, Dst: dst, Dev: names[r.LinkIndex], Src: src.Unmap()})
	}
	return out, nil
}

func (k *Kernel) observeSysctls(devs []string, disableIPv6 bool) (map[string]string, error) {
	out := map[string]string{}
	for _, dev := range devs {
		for _, sc := range InterfaceSysctls(dev, disableIPv6) {
			b, err := os.ReadFile(filepath.Join(ProcNet, sc.Path))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, fmt.Errorf("read sysctl %s: %w", sc.Path, err)
			}
			out[sc.Path] = strings.TrimSpace(string(b))
		}
	}
	return out, nil
}

func ruleOf(r netlink.Rule) desired.Rule {
	src, _ := prefixOf(r.Src)
	dst, _ := prefixOf(r.Dst)
	return desired.Rule{Priority: r.Priority, Src: src, Dst: dst, IIF: r.IifName, Table: r.Table}
}

func selectorless(r netlink.Rule) bool {
	return r.Src == nil && r.Dst == nil && r.IifName == "" && r.OifName == "" &&
		r.Mark == 0 && r.Tos == 0 && r.IPProto == 0 && r.Dport == nil && r.Sport == nil &&
		r.UIDRange == nil && !r.Invert
}

func prefixOf(n *net.IPNet) (netip.Prefix, bool) {
	if n == nil {
		return netip.Prefix{}, false
	}
	addr, ok := netip.AddrFromSlice(n.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	bits, _ := n.Mask.Size()
	return netip.PrefixFrom(addr.Unmap(), bits), true
}
