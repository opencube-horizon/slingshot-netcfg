package desired

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/opencube-horizon/slingshot-netcfg/internal/config"
	"github.com/opencube-horizon/slingshot-netcfg/internal/craytlv"
	"github.com/opencube-horizon/slingshot-netcfg/internal/lldpd"
)

func fixtureNeighbors(t *testing.T, name, iface string) []lldpd.Neighbor {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "lldpcli", name))
	if err != nil {
		t.Fatal(err)
	}
	all, err := lldpd.DecodeNeighbors(b)
	if err != nil {
		t.Fatal(err)
	}
	var out []lldpd.Neighbor
	for _, n := range all {
		if n.Interface == iface {
			out = append(out, n)
		}
	}
	return out
}

func crayNeighbor(chassis lldpd.ID, info string) lldpd.Neighbor {
	return lldpd.Neighbor{
		Interface: "hsn0", ChassisID: chassis, PortID: chassis,
		Custom: []lldpd.CustomTLV{{OUI: [3]byte{0x00, 0x0e, 0xab}, Subtype: 1, Info: []byte(info)}},
	}
}

var defaults = config.Default()

var netipEqual = cmpopts.EquateComparable(netip.Addr{}, netip.Prefix{})

func TestDeriveTarget_fromNodeCaptures(t *testing.T) {
	cases := []struct {
		fixture, mac, prefix string
	}{
		{"g100n032-neighbors.json", "02:00:00:00:00:37", "172.24.0.7/16"},
		{"cn01-neighbors.json", "02:00:00:00:00:10", "10.115.3.3/24"},
	}
	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			got, err := DeriveTarget(fixtureNeighbors(t, tc.fixture, "hsn0"), defaults.OrgTLV, defaults.MAC)
			if err != nil {
				t.Fatalf("DeriveTarget: %v", err)
			}
			if got.MAC.String() != tc.mac || got.Payload.Prefix != netip.MustParsePrefix(tc.prefix) ||
				got.Payload.MTU != 9000 || got.Payload.Lifetime != craytlv.Forever || got.Payload.QoSProfile != "ll_be_bd_et" {
				t.Errorf("target = %+v", got)
			}
		})
	}
}

func TestDeriveTarget_legacyMockSuccess(t *testing.T) {
	n := crayNeighbor(lldpd.ID{Type: "mac", Value: "02:fe:00:00:08:b3"}, `{ "ip_addr":"10.253.0.34/16","ttl":"forever","mtu": 9000}`)
	got, err := DeriveTarget([]lldpd.Neighbor{n}, defaults.OrgTLV, defaults.MAC)
	if err != nil || got.MAC.String() != "02:00:00:00:08:b3" || got.Payload.Prefix != netip.MustParsePrefix("10.253.0.34/16") {
		t.Errorf("target = %+v, %v", got, err)
	}
}

func TestDeriveTarget_keepsSecondOctet_whenMaskingDisabled(t *testing.T) {
	n := crayNeighbor(lldpd.ID{Type: "mac", Value: "02:fe:00:00:08:b3"}, `{"ip_addr":"10.0.0.1/8","ttl":"forever","mtu":9000}`)
	got, err := DeriveTarget([]lldpd.Neighbor{n}, defaults.OrgTLV, config.MAC{MaskSecondOctet: false})
	if err != nil || got.MAC.String() != "02:fe:00:00:08:b3" {
		t.Errorf("target = %+v, %v", got, err)
	}
}

func TestDeriveTarget_fallsBackToPortID_whenChassisIsNotMAC(t *testing.T) {
	n := crayNeighbor(lldpd.ID{Type: "local", Value: "sw1"}, `{"ip_addr":"10.0.0.1/8","ttl":"forever","mtu":9000}`)
	n.PortID = lldpd.ID{Type: "mac", Value: "02:fe:00:00:00:01"}
	got, err := DeriveTarget([]lldpd.Neighbor{n}, defaults.OrgTLV, defaults.MAC)
	if err != nil || got.MAC.String() != "02:00:00:00:00:01" {
		t.Errorf("target = %+v, %v", got, err)
	}
}

func TestDeriveTarget_errors(t *testing.T) {
	valid := `{"ip_addr":"10.0.0.1/8","ttl":"forever","mtu":9000}`
	mac := lldpd.ID{Type: "mac", Value: "02:fe:00:00:00:01"}
	cases := map[string]struct {
		neighbors []lldpd.Neighbor
		want      error
	}{
		"legacy missing-oui": {[]lldpd.Neighbor{{Interface: "hsn0", ChassisID: mac}}, ErrNoCrayTLV},
		"foreign OUI only": {[]lldpd.Neighbor{{
			Interface: "hsn0", ChassisID: mac,
			Custom: []lldpd.CustomTLV{{OUI: [3]byte{0x00, 0x16, 0xb9}, Subtype: 1, Info: []byte(valid)}},
		}}, ErrNoCrayTLV},
		"conflicting": {[]lldpd.Neighbor{
			crayNeighbor(mac, valid),
			crayNeighbor(mac, `{"ip_addr":"10.0.0.2/8","ttl":"forever","mtu":9000}`),
		}, ErrConflicting},
		"no MAC":        {[]lldpd.Neighbor{crayNeighbor(lldpd.ID{Type: "local", Value: "x"}, valid)}, ErrNoMAC},
		"multicast AMA": {[]lldpd.Neighbor{crayNeighbor(lldpd.ID{Type: "mac", Value: "01:fe:00:00:00:01"}, valid)}, ErrBadMAC},
		"zero AMA":      {[]lldpd.Neighbor{crayNeighbor(lldpd.ID{Type: "mac", Value: "00:fe:00:00:00:00"}, valid)}, ErrBadMAC},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DeriveTarget(tc.neighbors, defaults.OrgTLV, defaults.MAC); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestDeriveTarget_acceptsIdenticalTLVsFromSeveralNeighbors(t *testing.T) {
	valid := `{"ip_addr":"10.0.0.1/8","ttl":"forever","mtu":9000}`
	mac := lldpd.ID{Type: "mac", Value: "02:fe:00:00:00:01"}
	if _, err := DeriveTarget([]lldpd.Neighbor{crayNeighbor(mac, valid), crayNeighbor(mac, valid)}, defaults.OrgTLV, defaults.MAC); err != nil {
		t.Errorf("err = %v, want nil", err)
	}
}

func TestDeriveTarget_propagatesFieldError(t *testing.T) {
	n := crayNeighbor(lldpd.ID{Type: "mac", Value: "02:fe:00:00:00:01"}, `{"ttl":"forever","mtu":9000}`)
	_, err := DeriveTarget([]lldpd.Neighbor{n}, defaults.OrgTLV, defaults.MAC)
	var fe *craytlv.FieldError
	if !errors.As(err, &fe) || fe.Field != "ip_addr" {
		t.Errorf("err = %v, want FieldError on ip_addr", err)
	}
}

func TestRulesAndRoutes_matchLegacyLayout(t *testing.T) {
	addrs := []Addr{
		{Prefix: netip.MustParsePrefix("10.1.0.5/16"), Dev: "hsn0", N: 0},
		{Prefix: netip.MustParsePrefix("10.1.0.6/16"), Dev: "hsn1", N: 1},
	}
	host := func(s string) netip.Prefix { return netip.MustParsePrefix(s + "/32") }
	wantRules := []Rule{
		{Priority: 0, Src: host("10.1.0.5"), Dst: host("10.1.0.5"), Table: TableLocal},
		{Priority: 1, Src: host("10.1.0.5"), IIF: "hsn1", Table: TableLocal},
		{Priority: 2, Src: host("10.1.0.5"), Table: 200},
		{Priority: 0, Src: host("10.1.0.6"), Dst: host("10.1.0.6"), Table: TableLocal},
		{Priority: 1, Src: host("10.1.0.6"), IIF: "hsn0", Table: TableLocal},
		{Priority: 2, Src: host("10.1.0.6"), Table: 201},
	}
	if diff := cmp.Diff(wantRules, Rules(addrs, []string{"hsn0", "hsn1"}, 200), netipEqual); diff != "" {
		t.Errorf("rules (-want +got):\n%s", diff)
	}
	wantRoutes := []Route{
		{Table: 200, Dst: netip.MustParsePrefix("10.1.0.0/16"), Dev: "hsn0", Src: netip.MustParseAddr("10.1.0.5")},
		{Table: 201, Dst: netip.MustParsePrefix("10.1.0.0/16"), Dev: "hsn1", Src: netip.MustParseAddr("10.1.0.6")},
	}
	if diff := cmp.Diff(wantRoutes, Routes(addrs, 200), netipEqual); diff != "" {
		t.Errorf("routes (-want +got):\n%s", diff)
	}
}
