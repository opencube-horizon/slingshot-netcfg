package reconcile

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"time"

	"github.com/opencube-horizon/slingshot-netcfg/internal/craytlv"
	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
	"github.com/opencube-horizon/slingshot-netcfg/internal/discover"
	"github.com/opencube-horizon/slingshot-netcfg/internal/netapply"
	"github.com/opencube-horizon/slingshot-netcfg/internal/status"
)

// Diagnostic hints of §6.11.
const (
	hintLLDPD       = "check that lldpd is running (ext-lldpd on Talos, lldpd.service on distributions)"
	hintNotHandled  = "the lldpd interface pattern must include hsn*"
	hintNoLLDPGroup = "restart lldpd; it joins the group when it starts handling the interface"
	hintNoCarrier   = "check cabling and the local adapter state; carrier may be absent"
	hintNoNeighbor  = "check the Rosetta switch LLDP configuration and whether it advertises on other ports"
	hintNoCrayTLV   = "fabric configuration not active or not advertised on this port, or lldpd lacks custom-TLV support"
	hintInvalidTLV  = "check the Rosetta LLDP configuration"
	hintContention  = "check that Talos ignores the link or NetworkManager leaves it unmanaged, and that slingshot-network-config is not installed"
	noNeighborAfter = 60 * time.Second
)

func hintFor(err error) string {
	var fe *craytlv.FieldError
	switch {
	case errors.Is(err, errNoLLDPGroup):
		return hintNoLLDPGroup
	case errors.Is(err, errNotHandled):
		return hintNotHandled
	case errors.Is(err, desired.ErrNoCrayTLV):
		return hintNoCrayTLV
	case errors.As(err, &fe), errors.Is(err, craytlv.ErrMalformed), errors.Is(err, desired.ErrConflicting),
		errors.Is(err, desired.ErrNoMAC), errors.Is(err, desired.ErrBadMAC):
		return hintInvalidTLV
	default:
		return ""
	}
}

func sameTarget(a, b *desired.Target) bool {
	if a == nil || b == nil {
		return a == b
	}
	return bytes.Equal(a.MAC, b.MAC) && a.Payload.Prefix == b.Payload.Prefix &&
		a.Payload.MTU == b.Payload.MTU && a.Payload.Lifetime == b.Payload.Lifetime
}

func configOf(t *desired.Target) *status.Config {
	if t == nil {
		return nil
	}
	return &status.Config{MAC: t.MAC.String(), Addr: t.Payload.Prefix.String(), MTU: t.Payload.MTU, TTL: t.Payload.Lifetime.String()}
}

// writeReady writes hsnN.ready on first provisioning and whenever its content changes (§6.9).
func (r *Reconciler) writeReady(i discover.Iface, s *ifaceState) {
	if s.phase != PhaseProvisioned || r.DryRun {
		return
	}
	c := configOf(s.target)
	if s.ready != nil && *s.ready == *c {
		return
	}
	if err := r.Status.WriteReady(i.Link.Name, i.PCI, *c); err != nil {
		r.Log.Error("write readiness file", "ifname", i.Link.Name, "err", err)
		return
	}
	if s.ready == nil {
		r.Log.Info("readiness file written", "ifname", i.Link.Name)
	}
	s.ready = c
}

func (r *Reconciler) writeState(sel []tracked, st netapply.State) {
	if r.DryRun {
		return
	}
	doc := status.Document{LLDPD: status.LLDPD{Reachable: r.snapshot != nil && r.lldpdErr == nil, LastPoll: r.lastPoll, LastError: errText(r.lldpdErr)}}
	for _, t := range sel {
		s, name := t.state, t.Link.Name
		e := firstErr(s.lastErr, s.viewErr)
		if s.contended && e == nil {
			e = errors.New("another agent is modifying " + name)
		}
		entry := status.Interface{Ifname: name, PCI: t.PCI, Phase: string(s.phase), LastError: withHint(e, s), Neighbor: s.neighbor, Target: configOf(s.target)}
		if s.phase == PhaseProvisioned {
			entry.Applied = configOf(s.target)
		} else if addrs := st.Addrs[name]; len(addrs) > 0 {
			entry.Applied = &status.Config{MAC: t.Link.MAC.String(), Addr: addrs[0].Prefix.String(), MTU: t.Link.MTU}
		}
		doc.Interfaces = append(doc.Interfaces, entry)
	}
	b, err := json.Marshal(doc)
	if err != nil {
		r.Log.Error("encode state", "err", err)
		return
	}
	if string(b) == r.lastDoc {
		return
	}
	if err := r.Status.WriteState(doc); err != nil {
		r.Log.Error("write state.json", "err", err)
		return
	}
	r.lastDoc = string(b)
}

func withHint(err error, s *ifaceState) string {
	switch {
	case err != nil:
		if h := hintFor(err); h != "" {
			return err.Error() + " (" + h + ")"
		}
		return err.Error()
	case !s.running:
		return "no carrier (" + hintNoCarrier + ")"
	case s.phase == PhaseWaitingNeighbor && s.neighbor == nil:
		return "no LLDP neighbor (" + hintNoNeighbor + ")"
	default:
		return ""
	}
}
