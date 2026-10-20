// Package netapply observes the kernel network state relevant to
// slingshot-netcfg and executes planned operations on it (spec §6.6–§6.8).
// Observation and execution are Linux-only; the types are portable so the
// planner can be tested anywhere.
package netapply

import (
	"net"
	"net/netip"

	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
)

// Link is the observed state of one network interface.
type Link struct {
	Index       int
	Name        string
	Kind        string // netlink kind, e.g. "vlan"; empty for physical devices
	ParentIndex int
	MAC         net.HardwareAddr
	MTU         int
	Up          bool // administratively up (IFF_UP)
	Running     bool // carrier (IFF_RUNNING)
}

// Addr is an observed global-scope IPv4 address.
type Addr struct {
	Prefix netip.Prefix
	// ValidLft is the remaining valid lifetime in seconds; 0xffffffff is forever.
	ValidLft uint32
}

// State is the observed kernel state for one pass.
type State struct {
	Links []Link
	// Addrs maps an interface name to its global IPv4 addresses.
	Addrs map[string][]Addr
	// Rules and Routes are the IPv4 rules and routes carrying the ownership tag.
	Rules  []desired.Rule
	Routes []desired.Route
	// LocalRules are the selector-less IPv4 rules pointing to table local.
	LocalRules []LocalRule
	// Sysctls maps a path below /proc/sys/net (e.g. "ipv4/conf/hsn0/rp_filter")
	// to its current value.
	Sysctls map[string]string
}

// LocalRule is a selector-less `lookup local` rule. Its protocol is needed
// to delete exactly this rule: the kernel treats unset selectors in a delete
// request as wildcards.
type LocalRule struct {
	Priority int
	Protocol uint8
}

// Sysctl is one per-interface sysctl setting (§6.8).
type Sysctl struct {
	Path  string
	Value string
}

// InterfaceSysctls returns the §6.8 settings for one HSN interface or HSN
// VLAN interface.
func InterfaceSysctls(ifname string, disableIPv6 bool) []Sysctl {
	v4 := "ipv4/conf/" + ifname + "/"
	out := []Sysctl{
		{v4 + "accept_local", "1"},
		{v4 + "arp_accept", "1"},
		{v4 + "arp_ignore", "1"},
		{v4 + "arp_filter", "1"},
		{v4 + "arp_announce", "2"},
		{v4 + "arp_notify", "1"},
		{v4 + "rp_filter", "2"},
	}
	if disableIPv6 {
		out = append(out, Sysctl{"ipv6/conf/" + ifname + "/disable_ipv6", "1"})
	}
	return out
}
