package reconcile

import (
	"encoding/json/v2"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
	"github.com/opencube-horizon/slingshot-netcfg/internal/discover"
	"github.com/opencube-horizon/slingshot-netcfg/internal/netapply"
	"github.com/opencube-horizon/slingshot-netcfg/internal/status"
)

// fakeKernel is an in-memory kernel: Apply mutates the state that Observe
// returns. fail makes every operation of the given kind fail.
type fakeKernel struct {
	mu      sync.Mutex
	links   []netapply.Link
	st      netapply.State
	fail    func(netapply.Op) error
	applied []netapply.Op
	passes  int
}

func newFakeKernel() *fakeKernel {
	return &fakeKernel{
		links: []netapply.Link{hsn0.Link},
		st:    netapply.State{Addrs: map[string][]netapply.Addr{}, Sysctls: map[string]string{}, LocalRules: localRules(0, 32766, 32767), LLDPGroup: map[string]bool{"hsn0": true}},
	}
}

func (k *fakeKernel) ObserveLinks() ([]netapply.Link, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.passes++
	return slices.Clone(k.links), nil
}

func (k *fakeKernel) Observe(links []netapply.Link, _ []string, _ bool) (netapply.State, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	s := k.st
	s.Links = links
	s.Addrs = map[string][]netapply.Addr{}
	for n, a := range k.st.Addrs {
		s.Addrs[n] = slices.Clone(a)
	}
	s.Rules, s.Routes, s.LocalRules = slices.Clone(k.st.Rules), slices.Clone(k.st.Routes), slices.Clone(k.st.LocalRules)
	s.LLDPGroup = maps.Clone(k.st.LLDPGroup)
	return s, nil
}

func (k *fakeKernel) Apply(op netapply.Op) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail != nil {
		if err := k.fail(op); err != nil {
			return err
		}
	}
	k.applied = append(k.applied, op)
	switch o := op.(type) {
	case netapply.SetSysctl:
		k.st.Sysctls[o.Path] = o.Value
	case netapply.SetLink:
		if o.MAC != nil {
			k.links[0].MAC = o.MAC
		}
		if o.MTU != 0 {
			k.links[0].MTU = o.MTU
		}
	case netapply.LinkUp:
		k.links[0].Up = true
	case netapply.AddLocalRule:
		k.st.LocalRules = append(k.st.LocalRules, netapply.LocalRule{Priority: o.Priority})
	case netapply.DelLocalRule:
		k.st.LocalRules = slices.DeleteFunc(k.st.LocalRules, func(r netapply.LocalRule) bool { return r == o.Rule })
	case netapply.AddRule:
		k.st.Rules = append(k.st.Rules, o.Rule)
	case netapply.DelRule:
		k.st.Rules = slices.DeleteFunc(k.st.Rules, func(r desired.Rule) bool { return r == o.Rule })
	case netapply.ReplaceAddr:
		k.st.Addrs[o.Name] = []netapply.Addr{{Prefix: o.Prefix, ValidLft: uint32(o.Lifetime)}}
	case netapply.DelAddr:
		k.st.Addrs[o.Name] = slices.DeleteFunc(k.st.Addrs[o.Name], func(a netapply.Addr) bool { return a.Prefix == o.Prefix })
	case netapply.ReplaceRoute:
		k.st.Routes = append(k.st.Routes, o.Route)
	case netapply.DelRoute:
		k.st.Routes = slices.DeleteFunc(k.st.Routes, func(r desired.Route) bool { return r == o.Route })
	case netapply.FlushNeighbors:
	default:
		return errors.New("fake kernel: unhandled op")
	}
	return nil
}

func (k *fakeKernel) count(match func(netapply.Op) bool) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	n := 0
	for _, op := range k.applied {
		if match(op) {
			n++
		}
	}
	return n
}

func (k *fakeKernel) setMTU(mtu int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.links[0].MTU = mtu
}

func (k *fakeKernel) addr() []netapply.Addr {
	k.mu.Lock()
	defer k.mu.Unlock()
	return slices.Clone(k.st.Addrs["hsn0"])
}

// cassiniSelector is the production selector over a sysfs tree in which
// hsn0 is a Cassini NIC at PCI 0003:01:00.0.
func cassiniSelector(t *testing.T) discover.Selector {
	t.Helper()
	root := t.TempDir()
	pci := filepath.Join(root, "devices", "pci0003:00", "0003:01:00.0")
	netDir := filepath.Join(root, "class", "net", "hsn0")
	for _, d := range []string{pci, netDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, v := range map[string]string{"vendor": "0x17db\n", "device": "0x0501\n"} {
		if err := os.WriteFile(filepath.Join(pci, name), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(pci, filepath.Join(netDir, "device")); err != nil {
		t.Fatal(err)
	}
	return discover.Selector{SysRoot: root, Pattern: cfg.Interfaces.NamePattern, PCIIDs: cfg.Interfaces.PCIIDs}
}

// lastPhase reads hsn0's phase from state.json in dir.
func lastPhase(t *testing.T, dir string) string {
	t.Helper()
	var doc status.Document
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return ""
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Interfaces) == 0 {
		return ""
	}
	return doc.Interfaces[0].Phase
}
