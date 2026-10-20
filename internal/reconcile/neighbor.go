package reconcile

import (
	"bytes"
	"encoding/json/jsontext"
	"maps"
	"slices"
	"time"

	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
	"github.com/opencube-horizon/slingshot-netcfg/internal/status"
)

// noteNeighbor records the interface's neighbor and target from its view and
// logs what changed (§6.11).
func (r *Reconciler) noteNeighbor(name string, s *ifaceState, v View, now time.Time) {
	n := neighborOf(v, now)
	r.logNeighborChange(name, s.neighbor, n)
	r.trackNoNeighbor(name, s, n != nil, now)
	s.neighbor = n
	if v.Target != nil {
		r.logTargetChange(name, s.target, v.Target)
		s.target = v.Target
	}
}

func neighborOf(v View, now time.Time) *status.Neighbor {
	switch {
	case v.Target != nil:
		return &status.Neighbor{
			SysName: v.Target.SysName, PortDescription: v.Target.PortDescription, LastSeen: now,
			QoSProfile: v.Target.Payload.QoSProfile, CrayExtra: v.Target.Payload.Extra,
		}
	case len(v.Neighbors) > 0:
		first := v.Neighbors[0]
		return &status.Neighbor{SysName: first.SysName, PortDescription: first.PortDescription, LastSeen: now}
	default:
		return nil
	}
}

func (r *Reconciler) logNeighborChange(name string, old, n *status.Neighbor) {
	switch {
	case old == nil && n != nil:
		r.Log.Info("neighbor appeared", "ifname", name, "sysName", n.SysName, "port", n.PortDescription)
	case old != nil && n == nil:
		r.Log.Warn("neighbor disappeared", "ifname", name, "sysName", old.SysName)
	case old != nil && n != nil:
		if n.SysName != old.SysName || n.PortDescription != old.PortDescription {
			r.Log.Info("neighbor changed", "ifname", name, "sysName", n.SysName, "port", n.PortDescription)
		}
		if n.QoSProfile != old.QoSProfile {
			r.Log.Info("qosProfile changed", "ifname", name, "qosProfile", n.QoSProfile)
		}
		if !maps.EqualFunc(n.CrayExtra, old.CrayExtra, func(a, b jsontext.Value) bool { return bytes.Equal(a, b) }) {
			r.Log.Debug("Cray TLV extra keys", "ifname", name, "keys", slices.Sorted(maps.Keys(n.CrayExtra)))
		}
	}
}

// trackNoNeighbor warns once when an interface with carrier has had no
// neighbor for noNeighborAfter.
func (r *Reconciler) trackNoNeighbor(name string, s *ifaceState, hasNeighbor bool, now time.Time) {
	switch {
	case hasNeighbor || !s.running:
		s.noNeighborSince, s.noNeighborWarned = time.Time{}, false
	case s.noNeighborSince.IsZero():
		s.noNeighborSince = now
	case !s.noNeighborWarned && now.Sub(s.noNeighborSince) > noNeighborAfter:
		r.Log.Warn("no LLDP neighbor", "ifname", name, "for", now.Sub(s.noNeighborSince).Round(time.Second), "hint", hintNoNeighbor)
		s.noNeighborWarned = true
	}
}

func (r *Reconciler) logTargetChange(name string, old, n *desired.Target) {
	if !sameTarget(old, n) {
		r.Log.Info("target changed", "ifname", name, "from", configOf(old), "to", configOf(n))
	}
}
