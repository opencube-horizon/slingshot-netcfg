package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/opencube-horizon/slingshot-netcfg/internal/lldpd"
	"github.com/opencube-horizon/slingshot-netcfg/internal/netapply"
	"github.com/opencube-horizon/slingshot-netcfg/internal/status"
)

func g100n032Snapshot() lldpd.Result {
	n := lldpd.Neighbor{
		Interface: "hsn0",
		ChassisID: lldpd.ID{Type: "mac", Value: "02:fe:00:00:00:37"},
		PortID:    lldpd.ID{Type: "mac", Value: "02:fe:00:00:00:37"},
		SysName:   "x3000c0r0b0",
		Custom: []lldpd.CustomTLV{{
			OUI: [3]byte{0x00, 0x0e, 0xab}, Subtype: 1,
			Info: []byte(`{"ip_addr":"172.24.0.7/16","ttl":"forever","mtu":9000}`),
		}},
	}
	return lldpd.Result{Snapshot: &lldpd.Snapshot{
		Neighbors: map[string][]lldpd.Neighbor{"hsn0": {n}},
		Handled:   map[string]bool{"hsn0": true},
	}}
}

type harness struct {
	k       *fakeKernel
	runDir  string
	rec     *Reconciler
	results chan lldpd.Result
	trig    chan struct{}
	fast    chan bool
	cancel  context.CancelFunc
	done    chan error
}

func start(t *testing.T, k *fakeKernel, once bool) *harness {
	h := &harness{k: k, runDir: t.TempDir(), results: make(chan lldpd.Result, 1), trig: make(chan struct{}, 1), fast: make(chan bool, 1), done: make(chan error, 1)}
	h.rec = &Reconciler{Cfg: cfg, Kernel: k, Selector: cassiniSelector(t), Status: status.Writer{Dir: h.runDir}, Log: slog.New(slog.DiscardHandler), Once: once}
	ctx, cancel := context.WithCancel(t.Context())
	h.cancel = cancel
	go func() { h.done <- h.rec.Run(ctx, h.results, h.trig, h.fast) }()
	return h
}

func (h *harness) stop() { h.cancel(); <-h.done }

func TestRun_provisionsInterface_onFirstSnapshot(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, newFakeKernel(), false)
		defer h.stop()

		h.results <- g100n032Snapshot()
		synctest.Wait()

		if a := h.k.addr(); len(a) != 1 || a[0].Prefix.String() != "172.24.0.7/16" {
			t.Errorf("addresses = %v", a)
		}
		if p := lastPhase(t, h.runDir); p != string(PhaseProvisioned) {
			t.Errorf("phase = %q", p)
		}
		if ready, err := os.ReadFile(filepath.Join(h.runDir, "hsn0.ready")); err != nil || !strings.Contains(string(ready), "mac=02:00:00:00:00:37\n") {
			t.Errorf("hsn0.ready = %q, %v", ready, err)
		}
		if fast := <-h.fast; fast {
			t.Error("fast polling requested although provisioned")
		}
	})
}

func TestRun_coalescesTriggers_withinDebounce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, newFakeKernel(), false)
		defer h.stop()

		for range 5 {
			h.trig <- struct{}{}
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(time.Second)
		synctest.Wait()

		if h.k.passes != 1 {
			t.Errorf("passes = %d, want 1", h.k.passes)
		}
	})
}

func TestRun_backsOffExponentially_afterApplyFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		k := newFakeKernel()
		k.fail = func(op netapply.Op) error {
			if _, ok := op.(netapply.ReplaceAddr); ok {
				return errors.New("EPERM")
			}
			return nil
		}
		h := start(t, k, false)
		defer h.stop()
		h.results <- g100n032Snapshot()
		synctest.Wait()

		for range 80 {
			h.trig <- struct{}{}
			time.Sleep(100 * time.Millisecond)
		}
		synctest.Wait()

		attempts := k.count(func(op netapply.Op) bool { _, ok := op.(netapply.SetLink); return ok })
		if attempts != 1 {
			t.Errorf("SetLink applied %d times, want 1 (it succeeded)", attempts)
		}
		if p := lastPhase(t, h.runDir); p != string(PhaseError) {
			t.Errorf("phase = %q, want error", p)
		}
		// With 1, 2, 4 s backoff the address is retried at most 4 times in 8 s.
		var tries int
		k.fail = func(op netapply.Op) error {
			if _, ok := op.(netapply.ReplaceAddr); ok {
				tries++
				return errors.New("EPERM")
			}
			return nil
		}
		for range 80 {
			h.trig <- struct{}{}
			time.Sleep(100 * time.Millisecond)
		}
		synctest.Wait()
		if tries < 1 || tries > 3 {
			t.Errorf("address retries in 8 s with backoff = %d, want 1..3", tries)
		}
	})
}

func TestRun_reportsContention_andRateLimitsReapply(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		k := newFakeKernel()
		h := start(t, k, false)
		defer h.stop()
		h.results <- g100n032Snapshot()
		synctest.Wait()

		for range 10 {
			k.setMTU(1500)
			h.trig <- struct{}{}
			time.Sleep(2 * time.Second)
		}
		synctest.Wait()

		if p := lastPhase(t, h.runDir); p != string(PhaseError) {
			t.Errorf("phase = %q, want error (contention)", p)
		}
		mtuSets := k.count(func(op netapply.Op) bool { o, ok := op.(netapply.SetLink); return ok && o.MTU == 9000 })
		if mtuSets > 4 {
			t.Errorf("MTU re-applied %d times in 20 s, want at most 4 (rate limited to 30 s after contention)", mtuSets)
		}
	})
}

func TestRun_returns_whenOnceAndProvisioned(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, newFakeKernel(), true)
		h.results <- g100n032Snapshot()
		synctest.Wait()

		select {
		case err := <-h.done:
			if err != nil {
				t.Errorf("Run = %v", err)
			}
		default:
			t.Fatal("Run still running after provisioning with Once")
		}
		h.cancel()
	})
}

func TestRun_keepsConfiguration_whenLLDPDBecomesUnreachable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, newFakeKernel(), false)
		defer h.stop()
		h.results <- g100n032Snapshot()
		synctest.Wait()
		applied := h.k.count(func(netapply.Op) bool { return true })

		h.results <- lldpd.Result{Err: errors.New("connection refused")}
		time.Sleep(2 * time.Minute)
		synctest.Wait()

		if n := h.k.count(func(netapply.Op) bool { return true }); n != applied {
			t.Errorf("ops applied while lldpd unreachable: %d, want none", n-applied)
		}
		if p := lastPhase(t, h.runDir); p != string(PhaseProvisioned) {
			t.Errorf("phase = %q, want provisioned", p)
		}
	})
}
