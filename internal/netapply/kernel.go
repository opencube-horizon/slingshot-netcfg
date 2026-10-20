package netapply

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
)

// procNet holds the per-interface sysctls of §6.8.
const procNet = "/proc/sys/net"

// Kernel executes operations and observes state via netlink and /proc/sys/net.
type Kernel struct {
	Handle *netlink.Handle
	// Protocol is the ownership tag (routing.protocol).
	Protocol uint8
}

// Apply executes one operation.
func (k *Kernel) Apply(op Op) error {
	switch o := op.(type) {
	case SetSysctl:
		return os.WriteFile(filepath.Join(procNet, o.Path), []byte(o.Value), 0o644)
	case SetLink:
		return k.setLink(o)
	case LinkUp:
		return k.Handle.LinkSetUp(linkRef(o.Index))
	case AddLocalRule:
		return k.Handle.RuleAdd(localRule(LocalRule{Priority: o.Priority}))
	case DelLocalRule:
		return k.Handle.RuleDel(localRule(o.Rule))
	case AddRule:
		return k.Handle.RuleAdd(k.rule(o.Rule))
	case DelRule:
		return k.Handle.RuleDel(k.rule(o.Rule))
	case ReplaceAddr:
		lft := int(o.Lifetime)
		return k.Handle.AddrReplace(linkRef(o.Index), &netlink.Addr{IPNet: ipNet(o.Prefix), ValidLft: lft, PreferedLft: lft})
	case DelAddr:
		return k.Handle.AddrDel(linkRef(o.Index), &netlink.Addr{IPNet: ipNet(o.Prefix)})
	case ReplaceRoute:
		r, err := k.route(o.Route)
		if err != nil {
			return err
		}
		return k.Handle.RouteReplace(r)
	case DelRoute:
		r, err := k.route(o.Route)
		if err != nil {
			return err
		}
		return k.Handle.RouteDel(r)
	case FlushNeighbors:
		return k.flushNeighbors(o)
	}
	return fmt.Errorf("netapply: unhandled operation %T", op)
}

func (k *Kernel) setLink(o SetLink) error {
	link := linkRef(o.Index)
	if o.MAC != nil {
		err := k.Handle.LinkSetHardwareAddr(link, o.MAC)
		if refused(err) {
			return k.downWindow(o)
		}
		if err != nil {
			return fmt.Errorf("set MAC: %w", err)
		}
	}
	if o.MTU != 0 {
		err := k.Handle.LinkSetMTU(link, o.MTU)
		if refused(err) {
			return k.downWindow(SetLink{Name: o.Name, Index: o.Index, MTU: o.MTU})
		}
		if err != nil {
			return fmt.Errorf("set MTU: %w", err)
		}
	}
	return nil
}

// downWindow applies MAC/MTU with the link down, for drivers that refuse
// live changes (§6.6 step 2). The link is brought up again in any case.
func (k *Kernel) downWindow(o SetLink) (err error) {
	link := linkRef(o.Index)
	if err := k.Handle.LinkSetDown(link); err != nil {
		return fmt.Errorf("link down: %w", err)
	}
	defer func() {
		if upErr := k.Handle.LinkSetUp(link); upErr != nil {
			err = errors.Join(err, fmt.Errorf("link up: %w", upErr))
		}
	}()
	if o.MAC != nil {
		if err := k.Handle.LinkSetHardwareAddr(link, o.MAC); err != nil {
			return fmt.Errorf("set MAC (link down): %w", err)
		}
	}
	if o.MTU != 0 {
		if err := k.Handle.LinkSetMTU(link, o.MTU); err != nil {
			return fmt.Errorf("set MTU (link down): %w", err)
		}
	}
	return nil
}

func refused(err error) bool {
	return errors.Is(err, unix.EBUSY) || errors.Is(err, unix.EOPNOTSUPP)
}

func (k *Kernel) rule(r desired.Rule) *netlink.Rule {
	nr := netlink.NewRule()
	nr.Family = netlink.FAMILY_V4
	nr.Priority = r.Priority
	nr.Table = r.Table
	nr.Src = ipNet(r.Src)
	if r.Dst.IsValid() {
		nr.Dst = ipNet(r.Dst)
	}
	nr.IifName = r.IIF
	nr.Protocol = k.Protocol
	return nr
}

func localRule(r LocalRule) *netlink.Rule {
	nr := netlink.NewRule()
	nr.Family = netlink.FAMILY_V4
	nr.Priority = r.Priority
	nr.Table = desired.TableLocal
	nr.Protocol = r.Protocol
	return nr
}

func (k *Kernel) route(r desired.Route) (*netlink.Route, error) {
	link, err := k.Handle.LinkByName(r.Dev)
	if err != nil {
		return nil, fmt.Errorf("route dev %s: %w", r.Dev, err)
	}
	return &netlink.Route{
		Family:    netlink.FAMILY_V4,
		LinkIndex: link.Attrs().Index,
		Dst:       ipNet(r.Dst),
		Src:       r.Src.AsSlice(),
		Table:     r.Table,
		Scope:     netlink.SCOPE_HOST,
		Protocol:  netlink.RouteProtocol(k.Protocol),
	}, nil
}

func (k *Kernel) flushNeighbors(o FlushNeighbors) error {
	dev, err := k.Handle.LinkByName(o.Dev)
	if err != nil {
		return fmt.Errorf("neighbor flush dev %s: %w", o.Dev, err)
	}
	neighs, err := k.Handle.NeighList(0, netlink.FAMILY_V4)
	if err != nil {
		return fmt.Errorf("list neighbors: %w", err)
	}
	var errs []error
	for _, n := range neighs {
		if n.State&(netlink.NUD_PERMANENT|netlink.NUD_NOARP) != 0 {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		if n.LinkIndex != dev.Attrs().Index && (!ok || !o.Prefix.Contains(ip.Unmap())) {
			continue
		}
		if err := k.Handle.NeighDel(&n); err != nil && !errors.Is(err, unix.ENOENT) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func linkRef(index int) netlink.Link {
	return &netlink.Device{LinkAttrs: netlink.LinkAttrs{Index: index}}
}

func ipNet(p netip.Prefix) *net.IPNet {
	return &net.IPNet{IP: p.Addr().AsSlice(), Mask: net.CIDRMask(p.Bits(), p.Addr().BitLen())}
}
