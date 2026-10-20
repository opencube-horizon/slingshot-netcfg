// Package status writes the status document state.json and the hsnN.ready
// readiness files (spec §6.9, §6.11). Both are written atomically.
package status

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Document is the content of state.json.
type Document struct {
	LLDPD      LLDPD       `json:"lldpd"`
	Interfaces []Interface `json:"interfaces"`
}

// LLDPD describes reachability of lldpd.
type LLDPD struct {
	Reachable bool      `json:"reachable"`
	LastPoll  time.Time `json:"lastPoll,omitzero"`
	LastError string    `json:"lastError,omitempty"`
}

// Interface is the status of one selected HSN interface.
type Interface struct {
	Ifname    string    `json:"ifname"`
	PCI       string    `json:"pci,omitempty"`
	Phase     string    `json:"phase"`
	LastError string    `json:"lastError,omitempty"`
	Neighbor  *Neighbor `json:"neighbor,omitempty"`
	Target    *Config   `json:"target,omitempty"`
	Applied   *Config   `json:"applied,omitempty"`
}

// Neighbor is the LLDP neighbor of an HSN interface.
type Neighbor struct {
	SysName         string                    `json:"sysName,omitempty"`
	PortDescription string                    `json:"portDescription,omitempty"`
	LastSeen        time.Time                 `json:"lastSeen,omitzero"`
	QoSProfile      string                    `json:"qosProfile,omitempty"`
	CrayExtra       map[string]jsontext.Value `json:"crayExtra,omitempty"`
}

// Config is an interface configuration (target or applied).
type Config struct {
	MAC  string `json:"mac,omitempty"`
	Addr string `json:"addr,omitempty"`
	MTU  int    `json:"mtu,omitzero"`
	TTL  string `json:"ttl,omitempty"`
}

// Writer writes into Dir (normally /run/slingshot-netcfg).
type Writer struct {
	Dir string
}

// WriteState writes state.json.
func (w Writer) WriteState(doc Document) error {
	b, err := json.Marshal(doc, jsontext.Multiline(true))
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	return w.writeAtomic("state.json", append(b, '\n'))
}

// WriteReady writes <ifname>.ready.
func (w Writer) WriteReady(ifname, pci string, c Config) error {
	var b strings.Builder
	for _, kv := range [][2]string{
		{"ifname", ifname},
		{"pci", pci},
		{"mac", c.MAC},
		{"addr", c.Addr},
		{"mtu", strconv.Itoa(c.MTU)},
		{"ttl", c.TTL},
	} {
		fmt.Fprintf(&b, "%s=%s\n", kv[0], kv[1])
	}
	return w.writeAtomic(ifname+".ready", []byte(b.String()))
}

func (w Writer) writeAtomic(name string, data []byte) error {
	if err := os.MkdirAll(w.Dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", w.Dir, err)
	}

	tmp, err := os.CreateTemp(w.Dir, "."+name+".*")
	if err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	// After a successful rename the temporary name is gone; removing it then fails harmlessly.
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", name, errors.Join(err, tmp.Close()))
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("write %s: %w", name, errors.Join(err, tmp.Close()))
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(w.Dir, name)); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}
