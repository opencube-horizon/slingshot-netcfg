package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// file mirrors the TOML schema of §6.13. It is pre-filled with the defaults,
// so keys absent from the file keep their default values.
type file struct {
	ResyncInterval duration `toml:"resync_interval"`
	Interfaces     struct {
		NamePattern pattern  `toml:"name_pattern"`
		PCIIDs      []string `toml:"pci_ids"`
	} `toml:"interfaces"`
	LLDPD struct {
		LLDPCLI        string   `toml:"lldpcli"`
		Socket         string   `toml:"socket"`
		PollInterval   duration `toml:"poll_interval"`
		CommandTimeout duration `toml:"command_timeout"`
	} `toml:"lldpd"`
	OrgTLV struct {
		OUI     oui `toml:"oui"`
		Subtype int `toml:"subtype"`
	} `toml:"org_tlv"`
	MAC struct {
		MaskSecondOctet bool `toml:"mask_second_octet"`
	} `toml:"mac"`
	Apply struct {
		FlushForeignAddrs bool `toml:"flush_foreign_addrs"`
	} `toml:"apply"`
	Routing struct {
		Enabled       bool `toml:"enabled"`
		TableBase     int  `toml:"table_base"`
		Protocol      int  `toml:"protocol"`
		MoveLocalRule bool `toml:"move_local_rule"`
		VLANs         bool `toml:"vlans"`
	} `toml:"routing"`
	IPv6 struct {
		Disable bool `toml:"disable"`
	} `toml:"ipv6"`
	Security struct {
		Landlock bool `toml:"landlock"`
	} `toml:"security"`
}

func defaultFile() file {
	var f file
	f.ResyncInterval = duration{60 * time.Second}
	f.Interfaces.NamePattern = pattern{regexp.MustCompile(`^hsn([0-9]+)$`)}
	f.Interfaces.PCIIDs = []string{"17db:0501"}
	f.LLDPD.LLDPCLI = "lldpcli"
	f.LLDPD.PollInterval = duration{3 * time.Second}
	f.LLDPD.CommandTimeout = duration{10 * time.Second}
	f.OrgTLV.OUI = oui{[3]byte{0x00, 0x0e, 0xab}}
	f.OrgTLV.Subtype = 1
	f.MAC.MaskSecondOctet = true
	f.Apply.FlushForeignAddrs = true
	f.Routing.Enabled = true
	f.Routing.TableBase = 200
	f.Routing.Protocol = 205
	f.Routing.MoveLocalRule = true
	f.Routing.VLANs = true
	f.IPv6.Disable = true
	f.Security.Landlock = true
	return f
}

type duration struct{ d time.Duration }

func (d *duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return fmt.Errorf("%w: duration %q: %w", ErrInvalid, b, err)
	}
	d.d = v
	return nil
}

type pattern struct{ re *regexp.Regexp }

func (p *pattern) UnmarshalText(b []byte) error {
	re, err := regexp.Compile(string(b))
	if err != nil {
		return fmt.Errorf("%w: name_pattern: %w", ErrInvalid, err)
	}
	p.re = re
	return nil
}

type oui struct{ b [3]byte }

func (o *oui) UnmarshalText(b []byte) error {
	v, err := parseOUI(string(b))
	if err != nil {
		return err
	}
	o.b = v
	return nil
}

// PCIID is a PCI vendor:device pair.
type PCIID struct {
	Vendor, Device uint16
}

// ParsePCIID parses "17db:0501" (hex, case-insensitive, optional 0x prefixes).
func ParsePCIID(s string) (PCIID, error) {
	vs, ds, ok := strings.Cut(s, ":")
	if !ok {
		return PCIID{}, fmt.Errorf("%w: PCI ID %q, want vendor:device", ErrInvalid, s)
	}
	v, err1 := parseHex16(vs)
	d, err2 := parseHex16(ds)
	if err1 != nil || err2 != nil {
		return PCIID{}, fmt.Errorf("%w: PCI ID %q, want hex vendor:device", ErrInvalid, s)
	}
	return PCIID{Vendor: v, Device: d}, nil
}

// ParseHex16 parses a sysfs-style hex value such as "0x17db".
func ParseHex16(s string) (uint16, error) { return parseHex16(s) }

func parseHex16(s string) (uint16, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	v, err := strconv.ParseUint(s, 16, 16)
	return uint16(v), err
}

func (id PCIID) String() string { return fmt.Sprintf("%04x:%04x", id.Vendor, id.Device) }
