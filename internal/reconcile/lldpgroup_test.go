package reconcile

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/opencube-horizon/slingshot-netcfg/internal/lldpd"
	"github.com/opencube-horizon/slingshot-netcfg/internal/status"
)

// handledWithoutNeighbor is a snapshot in which lldpd handles hsn0 but has
// received nothing on it.
func handledWithoutNeighbor() lldpd.Result {
	return lldpd.Result{Snapshot: &lldpd.Snapshot{Neighbors: map[string][]lldpd.Neighbor{}, Handled: map[string]bool{"hsn0": true}}}
}

func lastError(t *testing.T, dir string) string {
	t.Helper()
	var doc status.Document
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Interfaces[0].LastError
}

func TestRun_reportsMissingLLDPGroup_whenNoNeighbor(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		k := newFakeKernel()
		k.st.LLDPGroup = map[string]bool{}
		h := start(t, k, false)
		defer h.stop()

		h.results <- handledWithoutNeighbor()
		synctest.Wait()

		if p := lastPhase(t, h.runDir); p != string(PhaseError) {
			t.Errorf("phase = %q, want error", p)
		}
		if e := lastError(t, h.runDir); !strings.Contains(e, errNoLLDPGroup.Error()) || !strings.Contains(e, hintNoLLDPGroup) {
			t.Errorf("lastError = %q, want the missing group with its hint", e)
		}
	})
}

func TestRun_waitsForNeighbor_whenLLDPGroupJoined(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := start(t, newFakeKernel(), false)
		defer h.stop()

		h.results <- handledWithoutNeighbor()
		synctest.Wait()

		if p := lastPhase(t, h.runDir); p != string(PhaseWaitingNeighbor) {
			t.Errorf("phase = %q, want waiting-neighbor", p)
		}
	})
}

func TestRun_provisions_whenFramesArriveWithoutLLDPGroup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		k := newFakeKernel()
		k.st.LLDPGroup = map[string]bool{}
		h := start(t, k, false)
		defer h.stop()

		h.results <- g100n032Snapshot()
		synctest.Wait()

		if p := lastPhase(t, h.runDir); p != string(PhaseProvisioned) {
			t.Errorf("phase = %q, want provisioned (e.g. promiscuous NIC)", p)
		}
	})
}
