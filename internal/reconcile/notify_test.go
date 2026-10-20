package reconcile

import (
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/opencube-horizon/slingshot-netcfg/internal/lldpd"
	"github.com/opencube-horizon/slingshot-netcfg/internal/sdnotify"
)

// drain returns the notifications sent so far.
func drain(notes chan string) []string {
	var out []string
	for {
		select {
		case n := <-notes:
			out = append(out, n)
		default:
			return out
		}
	}
}

func TestRun_notifiesReadyAndStatus_afterFirstPass(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, newFakeKernel(), false)
		defer h.stop()

		h.results <- g100n032Snapshot()
		synctest.Wait()

		want := []string{sdnotify.Ready, sdnotify.Status("hsn0 provisioned")}
		if got := drain(h.notes); !slices.Equal(got, want) {
			t.Errorf("notifications = %q, want %q", got, want)
		}
	})
}

func TestRun_sendsNothing_whenStatusUnchanged(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, newFakeKernel(), false)
		defer h.stop()
		h.results <- g100n032Snapshot()
		synctest.Wait()
		drain(h.notes)

		for range 3 {
			h.trig <- struct{}{}
			time.Sleep(time.Second)
		}
		synctest.Wait()

		if got := drain(h.notes); len(got) != 0 {
			t.Errorf("notifications = %q, want none", got)
		}
	})
}

func TestRun_reportsStatus_whenLLDPDBecomesUnreachable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, newFakeKernel(), false)
		defer h.stop()
		h.results <- g100n032Snapshot()
		synctest.Wait()
		drain(h.notes)

		h.results <- lldpd.Result{Err: errors.New("connection refused")}
		synctest.Wait()

		want := []string{sdnotify.Status("lldpd unreachable, hsn0 provisioned")}
		if got := drain(h.notes); !slices.Equal(got, want) {
			t.Errorf("notifications = %q, want %q", got, want)
		}
	})
}

func TestRun_pingsWatchdog_atHalfTheTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, newFakeKernel(), false, func(r *Reconciler) { r.Watchdog = 30 * time.Second })
		defer h.stop()

		time.Sleep(2*time.Minute + time.Second)
		synctest.Wait()

		pings := 0
		for _, n := range drain(h.notes) {
			if n == sdnotify.Watchdog {
				pings++
			}
		}
		if pings != 8 {
			t.Errorf("watchdog pings in 2 min = %d, want 8 (every 15 s)", pings)
		}
	})
}

func TestRun_notifiesStopping_whenCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, newFakeKernel(), false)

		h.stop()

		if got := drain(h.notes); !slices.Equal(got, []string{sdnotify.Stopping}) {
			t.Errorf("notifications = %q, want STOPPING only", got)
		}
	})
}
