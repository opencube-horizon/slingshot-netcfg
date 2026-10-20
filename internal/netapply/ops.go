package netapply

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/opencube-horizon/slingshot-netcfg/internal/craytlv"
	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
)

// Op is one planned kernel change. The set of implementations is closed
// (sealed by the unexported method); gochecksumtype checks that every type
// switch over Op handles all of them.
//
//sumtype:decl
type Op interface {
	// Iface is the HSN interface the operation belongs to, or "" for
	// host-wide operations; failures are attributed to it (backoff).
	Iface() string
	String() string
	op()
}

// SetSysctl writes Value to /proc/sys/net/Path.
type SetSysctl struct {
	Dev   string // interface the setting belongs to
	Owner string // HSN interface for attribution (the parent of a VLAN)
	Sysctl
}

// SetLink changes MAC and/or MTU of an HSN interface. A nil MAC or zero MTU
// leaves that setting unchanged. Live change is tried first (§6.6 step 2).
type SetLink struct {
	Name  string
	Index int
	MAC   net.HardwareAddr
	MTU   int
}

// LinkUp sets an HSN interface administratively up.
type LinkUp struct {
	Name  string
	Index int
}

// AddLocalRule adds the untagged `pref Priority lookup local` rule.
type AddLocalRule struct{ Priority int }

// DelLocalRule deletes one selector-less `lookup local` rule.
type DelLocalRule struct{ Rule LocalRule }

// AddRule adds a tagged policy rule.
type AddRule struct{ Rule desired.Rule }

// DelRule deletes a tagged policy rule.
type DelRule struct{ Rule desired.Rule }

// ReplaceAddr adds or replaces the target address with the given lifetime.
// Refresh marks a pure lifetime refresh of an already present address.
type ReplaceAddr struct {
	Name     string
	Index    int
	Prefix   netip.Prefix
	Lifetime craytlv.Lifetime
	Refresh  bool
}

// DelAddr deletes a foreign IPv4 address from an HSN interface.
type DelAddr struct {
	Name   string
	Index  int
	Prefix netip.Prefix
}

// ReplaceRoute adds or replaces a tagged table route.
type ReplaceRoute struct {
	Owner string
	Route desired.Route
}

// DelRoute deletes a tagged table route.
type DelRoute struct{ Route desired.Route }

// FlushNeighbors removes non-permanent IPv4 neighbor entries on Dev and, on
// any interface, those whose address lies inside Prefix (§6.8).
type FlushNeighbors struct {
	Owner  string
	Dev    string
	Prefix netip.Prefix
}

// Iface implements Op.
func (o SetSysctl) Iface() string { return o.Owner }

// Iface implements Op.
func (o SetLink) Iface() string { return o.Name }

// Iface implements Op.
func (o LinkUp) Iface() string { return o.Name }

// Iface implements Op.
func (AddLocalRule) Iface() string { return "" }

// Iface implements Op.
func (DelLocalRule) Iface() string { return "" }

// Iface implements Op.
func (AddRule) Iface() string { return "" }

// Iface implements Op.
func (DelRule) Iface() string { return "" }

// Iface implements Op.
func (o ReplaceAddr) Iface() string { return o.Name }

// Iface implements Op.
func (o DelAddr) Iface() string { return o.Name }

// Iface implements Op.
func (o ReplaceRoute) Iface() string { return o.Owner }

// Iface implements Op.
func (DelRoute) Iface() string { return "" }

// Iface implements Op.
func (o FlushNeighbors) Iface() string { return o.Owner }

func (SetSysctl) op()      {}
func (SetLink) op()        {}
func (LinkUp) op()         {}
func (AddLocalRule) op()   {}
func (DelLocalRule) op()   {}
func (AddRule) op()        {}
func (DelRule) op()        {}
func (ReplaceAddr) op()    {}
func (DelAddr) op()        {}
func (ReplaceRoute) op()   {}
func (DelRoute) op()       {}
func (FlushNeighbors) op() {}

func (o SetSysctl) String() string { return fmt.Sprintf("sysctl %s=%s", o.Path, o.Value) }
func (o SetLink) String() string {
	return fmt.Sprintf("link %s set mac=%v mtu=%d", o.Name, o.MAC, o.MTU)
}
func (o LinkUp) String() string { return "link " + o.Name + " up" }
func (o AddLocalRule) String() string {
	return fmt.Sprintf("rule add pref %d lookup local", o.Priority)
}

func (o DelLocalRule) String() string {
	return fmt.Sprintf("rule del pref %d lookup local", o.Rule.Priority)
}
func (o AddRule) String() string { return "rule add " + ruleString(o.Rule) }
func (o DelRule) String() string { return "rule del " + ruleString(o.Rule) }
func (o ReplaceAddr) String() string {
	return fmt.Sprintf("addr replace %s dev %s lifetime %s", o.Prefix, o.Name, o.Lifetime)
}
func (o DelAddr) String() string      { return fmt.Sprintf("addr del %s dev %s", o.Prefix, o.Name) }
func (o ReplaceRoute) String() string { return "route replace " + routeString(o.Route) }
func (o DelRoute) String() string     { return "route del " + routeString(o.Route) }
func (o FlushNeighbors) String() string {
	return fmt.Sprintf("neigh flush dev %s and to %s", o.Dev, o.Prefix)
}

func ruleString(r desired.Rule) string {
	s := fmt.Sprintf("pref %d from %s", r.Priority, r.Src)
	if r.Dst.IsValid() {
		s += " to " + r.Dst.String()
	}
	if r.IIF != "" {
		s += " iif " + r.IIF
	}
	return s + fmt.Sprintf(" lookup %d", r.Table)
}

func routeString(r desired.Route) string {
	return fmt.Sprintf("table %d %s dev %s scope host src %s", r.Table, r.Dst, r.Dev, r.Src)
}
