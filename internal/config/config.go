// Package config loads and validates the slingshot-netcfg configuration file
// (spec §6.13). Defaults are compiled in; the TOML file only overrides them.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"regexp"
	"time"

	"github.com/BurntSushi/toml"
)

// DefaultPath is where the configuration file is looked up unless --config is given.
const DefaultPath = "/etc/slingshot-netcfg/config.toml"

// Config is the validated configuration.
type Config struct {
	ResyncInterval time.Duration
	Interfaces     Interfaces
	LLDPD          LLDPD
	OrgTLV         OrgTLV
	MAC            MAC
	Apply          Apply
	Routing        Routing
	IPv6           IPv6
}

// Interfaces selects HSN interfaces (§6.2).
type Interfaces struct {
	NamePattern *regexp.Regexp
	PCIIDs      []PCIID
}

// LLDPD configures access to lldpd (§6.3).
type LLDPD struct {
	LLDPCLI        string
	Socket         string
	PollInterval   time.Duration
	CommandTimeout time.Duration
}

// OrgTLV identifies the Cray TLV (§5).
type OrgTLV struct {
	OUI     [3]byte
	Subtype uint8
}

// MAC controls AMA derivation (§6.4).
type MAC struct {
	MaskSecondOctet bool
}

// Apply controls address handling (§6.6).
type Apply struct {
	FlushForeignAddrs bool
}

// Routing controls policy routing (§6.7).
type Routing struct {
	Enabled       bool
	TableBase     int
	Protocol      uint8
	MoveLocalRule bool
	VLANs         bool
}

// IPv6 controls IPv6 on HSN interfaces (§6.8).
type IPv6 struct {
	Disable bool
}

// Default returns the compiled-in defaults of §6.13.
func Default() Config {
	cfg, err := fromFile(defaultFile())
	if err != nil {
		panic(fmt.Sprintf("config: invalid compiled-in defaults: %v", err))
	}
	return cfg
}

// Load reads the TOML file at path on top of the defaults. A missing file
// yields the defaults; unknown keys, type mismatches and invalid values are errors.
func Load(path string) (Config, error) {
	f := defaultFile()
	md, err := toml.DecodeFile(path, &f)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		// toml.ParseError doesn't unwrap the errors of UnmarshalText, so
		// every decode failure (syntax, type mismatch, bad value) is ErrInvalid.
		return Config{}, fmt.Errorf("config %s: %w: %w", path, ErrInvalid, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return Config{}, fmt.Errorf("config %s: %w: %v", path, ErrUnknownKeys, undecoded)
	}
	cfg, err := fromFile(f)
	if err != nil {
		return Config{}, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// ErrUnknownKeys reports keys in the file that the schema doesn't know.
var ErrUnknownKeys = errors.New("unknown keys")

// ErrInvalid reports a value that decodes but is out of range.
var ErrInvalid = errors.New("invalid value")

func fromFile(f file) (Config, error) {
	if f.ResyncInterval.d <= 0 {
		return Config{}, fmt.Errorf("%w: resync_interval must be positive", ErrInvalid)
	}
	if f.LLDPD.PollInterval.d <= 0 || f.LLDPD.CommandTimeout.d <= 0 {
		return Config{}, fmt.Errorf("%w: lldpd durations must be positive", ErrInvalid)
	}
	if f.LLDPD.LLDPCLI == "" {
		return Config{}, fmt.Errorf("%w: lldpd.lldpcli must not be empty", ErrInvalid)
	}
	if f.OrgTLV.Subtype < 0 || f.OrgTLV.Subtype > 255 {
		return Config{}, fmt.Errorf("%w: org_tlv.subtype %d outside 0..255", ErrInvalid, f.OrgTLV.Subtype)
	}
	// 0..4 are reserved by the kernel (unspec, redirect, kernel, boot, static).
	if f.Routing.Protocol < 5 || f.Routing.Protocol > 255 {
		return Config{}, fmt.Errorf("%w: routing.protocol %d outside 5..255", ErrInvalid, f.Routing.Protocol)
	}
	if f.Routing.TableBase < 1 || f.Routing.TableBase > 1<<31-1-1024 {
		return Config{}, fmt.Errorf("%w: routing.table_base %d out of range", ErrInvalid, f.Routing.TableBase)
	}
	if f.Interfaces.NamePattern.re.NumSubexp() < 1 {
		return Config{}, fmt.Errorf("%w: interfaces.name_pattern needs a capture group for N", ErrInvalid)
	}
	ids := make([]PCIID, 0, len(f.Interfaces.PCIIDs))
	for _, s := range f.Interfaces.PCIIDs {
		id, err := ParsePCIID(s)
		if err != nil {
			return Config{}, err
		}
		ids = append(ids, id)
	}
	return Config{
		ResyncInterval: f.ResyncInterval.d,
		Interfaces:     Interfaces{NamePattern: f.Interfaces.NamePattern.re, PCIIDs: ids},
		LLDPD: LLDPD{
			LLDPCLI:        f.LLDPD.LLDPCLI,
			Socket:         f.LLDPD.Socket,
			PollInterval:   f.LLDPD.PollInterval.d,
			CommandTimeout: f.LLDPD.CommandTimeout.d,
		},
		OrgTLV: OrgTLV{OUI: f.OrgTLV.OUI.b, Subtype: uint8(f.OrgTLV.Subtype)},
		MAC:    MAC{MaskSecondOctet: f.MAC.MaskSecondOctet},
		Apply:  Apply{FlushForeignAddrs: f.Apply.FlushForeignAddrs},
		Routing: Routing{
			Enabled:       f.Routing.Enabled,
			TableBase:     f.Routing.TableBase,
			Protocol:      uint8(f.Routing.Protocol),
			MoveLocalRule: f.Routing.MoveLocalRule,
			VLANs:         f.Routing.VLANs,
		},
		IPv6: IPv6{Disable: f.IPv6.Disable},
	}, nil
}

// parseOUI parses a colon-separated OUI such as "00:0e:ab".
func parseOUI(s string) ([3]byte, error) {
	hw, err := net.ParseMAC(s + ":00:00:00")
	if err != nil || len(hw) != 6 {
		return [3]byte{}, fmt.Errorf("%w: OUI %q, want xx:xx:xx", ErrInvalid, s)
	}
	return [3]byte{hw[0], hw[1], hw[2]}, nil
}
