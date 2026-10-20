package desired

import "net/netip"

// Addr is an HSN address: an IPv4 address that gets policy routing.
type Addr struct {
	// Prefix is the address with its prefix length, e.g. 10.0.0.5/16.
	Prefix netip.Prefix
	// Dev is the interface carrying it (hsnN or one of its VLANs).
	Dev string
	// N is the index of the parent HSN interface.
	N int
}

// Rule is a policy routing rule (§6.7). Zero Dst and empty IIF mean "any".
type Rule struct {
	Priority int
	Src      netip.Prefix
	Dst      netip.Prefix
	IIF      string
	Table    int
}

// Route is a route in a per-interface table (§6.7).
type Route struct {
	Table int
	Dst   netip.Prefix
	Dev   string
	Src   netip.Addr
}

// TableLocal is the kernel's local routing table.
const TableLocal = 255

// Rules returns the rules for addrs. devices are all selected HSN interfaces
// and HSN VLAN interfaces (the T of §6.7).
func Rules(addrs []Addr, devices []string, tableBase int) []Rule {
	var out []Rule
	for _, a := range addrs {
		host := netip.PrefixFrom(a.Prefix.Addr(), 32)
		out = append(out, Rule{Priority: 0, Src: host, Dst: host, Table: TableLocal})
		for _, dev := range devices {
			if dev != a.Dev {
				out = append(out, Rule{Priority: 1, Src: host, IIF: dev, Table: TableLocal})
			}
		}
		out = append(out, Rule{Priority: 2, Src: host, Table: tableBase + a.N})
	}
	return out
}

// Routes returns the table routes for addrs.
func Routes(addrs []Addr, tableBase int) []Route {
	out := make([]Route, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, Route{Table: tableBase + a.N, Dst: a.Prefix.Masked(), Dev: a.Dev, Src: a.Prefix.Addr()})
	}
	return out
}
