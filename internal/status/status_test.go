package status

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReady_writesKeyValueLines(t *testing.T) {
	w := Writer{Dir: t.TempDir()}

	err := w.WriteReady("hsn0", "0003:01:00.0", Config{MAC: "02:00:00:00:00:37", Addr: "172.24.0.7/16", MTU: 9000, TTL: "forever"})
	if err != nil {
		t.Fatalf("WriteReady: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(w.Dir, "hsn0.ready"))
	want := "ifname=hsn0\npci=0003:01:00.0\nmac=02:00:00:00:00:37\naddr=172.24.0.7/16\nmtu=9000\nttl=forever\n"
	if string(got) != want {
		t.Errorf("hsn0.ready =\n%s\nwant\n%s", got, want)
	}
}

func TestWriteState_roundTrips_andLeavesNoTempFiles(t *testing.T) {
	w := Writer{Dir: t.TempDir()}
	doc := Document{
		LLDPD:      LLDPD{Reachable: true, LastPoll: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)},
		Interfaces: []Interface{{Ifname: "hsn0", Phase: "provisioned", Neighbor: &Neighbor{SysName: "x3000c0r0b0", QoSProfile: "ll_be_bd_et"}}},
	}

	if err := w.WriteState(doc); err != nil {
		t.Fatalf("WriteState: %v", err)
	}

	b, _ := os.ReadFile(filepath.Join(w.Dir, "state.json"))
	var back Document
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("state.json is not valid JSON: %v", err)
	}
	if back.Interfaces[0].Neighbor.SysName != "x3000c0r0b0" || !back.LLDPD.Reachable {
		t.Errorf("round trip = %+v", back)
	}
	entries, _ := os.ReadDir(w.Dir)
	if len(entries) != 1 {
		t.Errorf("directory has %d entries, want only state.json", len(entries))
	}
}
