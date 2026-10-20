package reconcile

import (
	"bytes"
	"time"

	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
	"github.com/opencube-horizon/slingshot-netcfg/internal/netapply"
	"github.com/opencube-horizon/slingshot-netcfg/internal/status"
)

// Phase is the per-interface phase of §6.10.
type Phase string

// Phases of §6.10.
const (
	PhaseWaitingCarrier  Phase = "waiting-carrier"
	PhaseWaitingNeighbor Phase = "waiting-neighbor"
	PhaseProvisioned     Phase = "provisioned"
	PhaseStale           Phase = "stale"
	PhaseError           Phase = "error"
)

const (
	initialBackoff    = time.Second
	maxBackoff        = 30 * time.Second
	contentionLimit   = 3
	contentionWindow  = 5 * time.Minute
	contentionBackoff = 30 * time.Second
	unhandledAfter    = 60 * time.Second
)

// ifaceState is what the reconciler remembers about one selected interface
// between passes.
type ifaceState struct {
	pci      string
	running  bool
	phase    Phase
	lastErr  error
	target   *desired.Target
	neighbor *status.Neighbor

	backoff   time.Duration
	notBefore time.Time

	appliedMAC  []byte
	appliedMTU  int
	appliedUp   bool
	appliedAddr string
	external    []time.Time
	contended   bool

	unhandledSince   time.Time
	noNeighborSince  time.Time
	noNeighborWarned bool
	viewErr          error
	ready            *status.Config
}

func (s *ifaceState) blocked(now time.Time) bool { return now.Before(s.notBefore) }

func (s *ifaceState) failed(now time.Time, err error) {
	s.lastErr = err
	s.backoff = min(max(2*s.backoff, initialBackoff), maxBackoff)
	s.notBefore = now.Add(s.backoff)
}

func (s *ifaceState) succeeded() { s.backoff = 0 }

// observeExternal counts a re-apply of a value this reconciler already
// applied successfully: someone else changed it (§6.1 contention).
func (s *ifaceState) observeExternal(op netapply.Op, now time.Time) {
	var again bool
	switch o := op.(type) {
	case netapply.SetLink:
		again = (o.MAC != nil && bytes.Equal(o.MAC, s.appliedMAC)) || (o.MTU != 0 && o.MTU == s.appliedMTU)
	case netapply.LinkUp:
		again = s.appliedUp
	case netapply.ReplaceAddr:
		again = !o.Refresh && o.Prefix.String() == s.appliedAddr
	default:
		// Other operations aren't settings another agent would revert.
	}
	if !again {
		return
	}
	s.external = append(s.external, now)
	s.pruneExternal(now)
}

func (s *ifaceState) pruneExternal(now time.Time) {
	cut := 0
	for cut < len(s.external) && now.Sub(s.external[cut]) > contentionWindow {
		cut++
	}
	s.external = s.external[cut:]
	s.contended = len(s.external) >= contentionLimit
}

func (s *ifaceState) recordApplied(op netapply.Op) {
	switch o := op.(type) {
	case netapply.SetLink:
		if o.MAC != nil {
			s.appliedMAC = o.MAC
		}
		if o.MTU != 0 {
			s.appliedMTU = o.MTU
		}
	case netapply.LinkUp:
		s.appliedUp = true
	case netapply.ReplaceAddr:
		s.appliedAddr = o.Prefix.String()
	default:
	}
}

// markConverged records the target of a provisioned interface as applied,
// so later re-applies count as external changes even if this process never
// had to apply them itself (e.g. after a restart).
func (s *ifaceState) markConverged() {
	if s.target == nil {
		return
	}
	s.appliedMAC, s.appliedMTU, s.appliedUp = s.target.MAC, s.target.Payload.MTU, true
	s.appliedAddr = s.target.Payload.Prefix.String()
}

func phaseOf(s *ifaceState, v View, hasAddr, pending bool) Phase {
	switch {
	case !s.running:
		return PhaseWaitingCarrier
	case v.Err != nil || s.lastErr != nil || s.contended:
		return PhaseError
	case v.Target != nil && !pending:
		return PhaseProvisioned
	case v.Known && v.Target == nil && hasAddr:
		return PhaseStale
	default:
		return PhaseWaitingNeighbor
	}
}
