// Package desired derives what an HSN interface should look like from its
// LLDP neighbors (spec §6.4) and which rules and routes its HSN addresses
// need (§6.5, §6.7).
package desired

import (
	"bytes"
	"errors"
	"fmt"
	"net"

	"github.com/opencube-horizon/slingshot-netcfg/internal/config"
	"github.com/opencube-horizon/slingshot-netcfg/internal/craytlv"
	"github.com/opencube-horizon/slingshot-netcfg/internal/lldpd"
)

// Target is the configuration an HSN interface gets from a valid neighbor.
type Target struct {
	MAC             net.HardwareAddr
	Payload         craytlv.Payload
	SysName         string
	PortDescription string
}

var (
	// ErrNoCrayTLV reports a neighbor that doesn't advertise the Cray TLV.
	ErrNoCrayTLV = errors.New("no Cray TLV advertised")
	// ErrConflicting reports several neighbors with differing Cray TLVs.
	ErrConflicting = errors.New("conflicting Cray TLVs")
	// ErrNoMAC reports a neighbor whose chassis and port IDs aren't MACs.
	ErrNoMAC = errors.New("no MAC in LLDP neighbor")
	// ErrBadMAC reports a derived AMA that is multicast or all-zero.
	ErrBadMAC = errors.New("derived MAC is multicast or all-zero")
)

// DeriveTarget computes the target from the LLDP neighbors of one interface.
func DeriveTarget(neighbors []lldpd.Neighbor, tlv config.OrgTLV, mac config.MAC) (Target, error) {
	var (
		chosen *lldpd.Neighbor
		info   []byte
	)
	for i := range neighbors {
		for _, c := range neighbors[i].Custom {
			if c.OUI != tlv.OUI || c.Subtype != int(tlv.Subtype) {
				continue
			}
			if chosen != nil && !bytes.Equal(info, c.Info) {
				return Target{}, ErrConflicting
			}
			chosen, info = &neighbors[i], c.Info //nolint:gosec // G602: i ranges over neighbors.
		}
	}
	if chosen == nil {
		return Target{}, ErrNoCrayTLV
	}
	hw, err := ama(*chosen, mac.MaskSecondOctet)
	if err != nil {
		return Target{}, err
	}
	payload, err := craytlv.Parse(info)
	if err != nil {
		return Target{}, err
	}
	return Target{MAC: hw, Payload: payload, SysName: chosen.SysName, PortDescription: chosen.PortDescription}, nil
}

func ama(n lldpd.Neighbor, mask bool) (net.HardwareAddr, error) {
	var raw string
	switch {
	case n.ChassisID.Type == "mac":
		raw = n.ChassisID.Value
	case n.PortID.Type == "mac":
		raw = n.PortID.Value
	default:
		return nil, ErrNoMAC
	}
	hw, err := net.ParseMAC(raw)
	if err != nil || len(hw) != 6 {
		return nil, fmt.Errorf("%w: %q", ErrNoMAC, raw)
	}
	if mask {
		hw[1] = 0
	}
	if hw[0]&1 == 1 || bytes.Equal(hw, make(net.HardwareAddr, 6)) {
		return nil, fmt.Errorf("%w: %s", ErrBadMAC, hw)
	}
	return hw, nil
}
