package reconcile

import (
	"slices"
	"strings"

	"github.com/opencube-horizon/slingshot-netcfg/internal/sdnotify"
)

// report tells systemd after each pass: READY once after the first pass,
// and STATUS whenever the summary of lldpd reachability and phases changes.
func (r *Reconciler) report() {
	if !r.ready {
		r.notify(sdnotify.Ready)
		r.ready = true
	}
	if s := r.statusLine(); s != r.lastStatus {
		r.notify(sdnotify.Status(s))
		r.lastStatus = s
	}
}

func (r *Reconciler) statusLine() string {
	var parts []string
	if r.lldpdErr != nil {
		parts = append(parts, "lldpd unreachable")
	}
	if len(r.ifs) == 0 {
		parts = append(parts, "no HSN interface selected")
	}
	lines := make([]string, 0, len(r.ifs))
	for name, s := range r.ifs {
		lines = append(lines, name+" "+string(s.phase))
	}
	slices.Sort(lines)
	return strings.Join(append(parts, lines...), ", ")
}

func (r *Reconciler) notify(state string) {
	if r.Notify == nil {
		return
	}
	if err := r.Notify(state); err != nil {
		r.Log.Warn("notify systemd", "err", err)
	}
}
