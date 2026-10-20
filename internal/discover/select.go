// Package discover selects the HSN interfaces (spec §6.2) and their VLAN
// interfaces (§6.2.1), and turns netlink link and address events into
// reconcile triggers.
package discover

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/opencube-horizon/slingshot-netcfg/internal/config"
	"github.com/opencube-horizon/slingshot-netcfg/internal/netapply"
)

// Iface is a selected HSN interface.
type Iface struct {
	Link netapply.Link
	N    int
	PCI  string
}

// VLAN is an HSN VLAN interface.
//
// The VLAN setup (§6.2.1) is untested on real systems: no HSN VLAN has been
// configured on Slingshot hardware yet, and how VLANs get created is still
// open (spec V-11 Talos, V-12 NetworkManager). Only unit tests and the netns
// integration test, with a dummy parent, cover it.
type VLAN struct {
	Link   netapply.Link
	N      int
	Parent string
}

// Selector selects HSN interfaces by name pattern and PCI vendor:device
// (§6.2), and their VLAN interfaces (§6.2.1).
type Selector struct {
	// SysRoot is the directory containing class/net (normally /sys).
	SysRoot string
	Pattern *regexp.Regexp
	PCIIDs  []config.PCIID
	// Only, if non-empty, restricts selection to these names (--iface).
	Only []string
}

var pciAddr = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

// Select returns the selected interfaces, ordered by N, and their VLANs.
func (s Selector) Select(links []netapply.Link) ([]Iface, []VLAN) {
	var ifaces []Iface
	byIndex := map[int]Iface{}
	for _, l := range links {
		if i, ok := s.pick(l); ok {
			ifaces = append(ifaces, i)
			byIndex[l.Index] = i
		}
	}
	var vlans []VLAN
	for _, l := range links {
		if parent, ok := byIndex[l.ParentIndex]; ok && l.Kind == "vlan" {
			vlans = append(vlans, VLAN{Link: l, N: parent.N, Parent: parent.Link.Name})
		}
	}
	slices.SortFunc(ifaces, func(a, b Iface) int { return a.N - b.N })
	slices.SortFunc(vlans, func(a, b VLAN) int { return strings.Compare(a.Link.Name, b.Link.Name) })
	return ifaces, vlans
}

func (s Selector) pick(l netapply.Link) (Iface, bool) {
	if l.Kind == "vlan" || (len(s.Only) > 0 && !slices.Contains(s.Only, l.Name)) {
		return Iface{}, false
	}
	n, ok := matchName(s.Pattern, l.Name)
	if !ok {
		return Iface{}, false
	}
	pci, id, err := s.resolvePCI(l.Name)
	if err != nil || !slices.Contains(s.PCIIDs, id) {
		return Iface{}, false
	}
	return Iface{Link: l, N: n, PCI: pci}, true
}

func matchName(re *regexp.Regexp, name string) (int, bool) {
	m := re.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	return n, err == nil
}

// resolvePCI finds the nearest PCI device above the interface's sysfs device
// (cxi_eth netdevs may hang off an auxiliary device) and reads its IDs.
// SR-IOV virtual functions are rejected: they never receive LLDP (§6.2).
func (s Selector) resolvePCI(ifname string) (string, config.PCIID, error) {
	dev, err := filepath.EvalSymlinks(filepath.Join(s.SysRoot, "class", "net", ifname, "device"))
	if err != nil {
		return "", config.PCIID{}, err
	}
	for d := dev; d != s.SysRoot && d != "/" && d != "."; d = filepath.Dir(d) {
		if !pciAddr.MatchString(filepath.Base(d)) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(d, "physfn")); err == nil {
			return "", config.PCIID{}, fmt.Errorf("%s is an SR-IOV virtual function", d)
		}
		vendor, err1 := readHex(filepath.Join(d, "vendor"))
		device, err2 := readHex(filepath.Join(d, "device"))
		if err1 != nil || err2 != nil {
			return "", config.PCIID{}, fmt.Errorf("read PCI IDs of %s: %w", d, errors.Join(err1, err2))
		}
		return filepath.Base(d), config.PCIID{Vendor: vendor, Device: device}, nil
	}
	return "", config.PCIID{}, fmt.Errorf("no PCI device above %s", dev)
}

func readHex(path string) (uint16, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return config.ParseHex16(string(b))
}
