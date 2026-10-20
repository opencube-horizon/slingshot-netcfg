package discover

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/opencube-horizon/slingshot-netcfg/internal/config"
	"github.com/opencube-horizon/slingshot-netcfg/internal/netapply"
)

// fakeSys builds a sysfs-like tree: class/net/<if>/device → a device below a
// PCI function with the given vendor/device files.
func fakeSys(t *testing.T, ifname, pci, vendor, device string, auxiliary bool) string {
	t.Helper()
	root := t.TempDir()
	pciDir := filepath.Join(root, "devices", "pci0003:00", "0003:00:00.0", pci)
	devDir := pciDir
	if auxiliary {
		devDir = filepath.Join(pciDir, "cxi_eth.0")
	}
	for _, d := range []string{devDir, filepath.Join(root, "class", "net", ifname)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, v := range map[string]string{"vendor": vendor, "device": device} {
		if err := os.WriteFile(filepath.Join(pciDir, name), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rel, _ := filepath.Rel(filepath.Join(root, "class", "net", ifname), devDir)
	if err := os.Symlink(rel, filepath.Join(root, "class", "net", ifname, "device")); err != nil {
		t.Fatal(err)
	}
	return root
}

func selector(root string) Selector {
	return Selector{SysRoot: root, Pattern: regexp.MustCompile(`^hsn([0-9]+)$`), PCIIDs: []config.PCIID{{Vendor: 0x17db, Device: 0x0501}}}
}

func TestSelector_selectsCassini_evenBehindAuxiliaryDevice(t *testing.T) {
	for _, aux := range []bool{false, true} {
		root := fakeSys(t, "hsn3", "0003:01:00.0", "0x17db", "0x0501", aux)
		links := []netapply.Link{{Index: 7, Name: "hsn3"}}

		ifaces, _ := selector(root).Select(links)

		if len(ifaces) != 1 || ifaces[0].N != 3 || ifaces[0].PCI != "0003:01:00.0" {
			t.Errorf("auxiliary=%v: ifaces = %+v", aux, ifaces)
		}
	}
}

func TestSelector_rejectsOtherPCIDevice(t *testing.T) {
	root := fakeSys(t, "hsn0", "0000:3b:00.0", "0x15b3", "0x1017", false)

	ifaces, _ := selector(root).Select([]netapply.Link{{Index: 2, Name: "hsn0"}})

	if len(ifaces) != 0 {
		t.Errorf("Mellanox device selected: %+v", ifaces)
	}
}

func TestSelector_rejectsVirtualFunction(t *testing.T) {
	root := fakeSys(t, "hsn1", "0003:01:00.1", "0x17db", "0x0501", true)
	pciDir := filepath.Join(root, "devices", "pci0003:00", "0003:00:00.0", "0003:01:00.1")
	if err := os.Symlink("../0003:01:00.0", filepath.Join(pciDir, "physfn")); err != nil {
		t.Fatal(err)
	}

	ifaces, _ := selector(root).Select([]netapply.Link{{Index: 2, Name: "hsn1"}})

	if len(ifaces) != 0 {
		t.Errorf("SR-IOV VF selected: %+v", ifaces)
	}
}

func TestSelector_rejectsTransientName(t *testing.T) {
	root := fakeSys(t, "eth5", "0003:01:00.0", "0x17db", "0x0501", false)

	ifaces, _ := selector(root).Select([]netapply.Link{{Index: 2, Name: "eth5"}})

	if len(ifaces) != 0 {
		t.Errorf("pre-rename name selected: %+v", ifaces)
	}
}

func TestSelector_honorsOnly(t *testing.T) {
	root := fakeSys(t, "hsn0", "0003:01:00.0", "0x17db", "0x0501", false)
	s := selector(root)
	s.Only = []string{"hsn1"}

	if ifaces, _ := s.Select([]netapply.Link{{Index: 2, Name: "hsn0"}}); len(ifaces) != 0 {
		t.Errorf("--iface filter ignored: %+v", ifaces)
	}
}

func TestSelect_findsVLANsOfSelectedInterfaces(t *testing.T) {
	root := fakeSys(t, "hsn0", "0003:01:00.0", "0x17db", "0x0501", false)
	links := []netapply.Link{
		{Index: 2, Name: "hsn0"},
		{Index: 3, Name: "hsn0.100", Kind: "vlan", ParentIndex: 2},
		{Index: 4, Name: "eth0"},
		{Index: 5, Name: "eth0.7", Kind: "vlan", ParentIndex: 4},
	}

	ifaces, vlans := selector(root).Select(links)

	if len(ifaces) != 1 || len(vlans) != 1 || vlans[0].Link.Name != "hsn0.100" || vlans[0].Parent != "hsn0" || vlans[0].N != 0 {
		t.Errorf("ifaces = %+v, vlans = %+v", ifaces, vlans)
	}
}
