package lldpd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "lldpcli", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodeNeighbors_g100n032(t *testing.T) {
	got, err := DecodeNeighbors(readFixture(t, "g100n032-neighbors.json"))
	if err != nil {
		t.Fatalf("DecodeNeighbors: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d neighbors, want 2", len(got))
	}
	hsn := got[1]
	want := Neighbor{
		Interface:       "hsn0",
		ChassisID:       ID{Type: "mac", Value: "02:fe:00:00:00:37"},
		PortID:          ID{Type: "mac", Value: "02:fe:00:00:00:37"},
		SysName:         "x3000c0r0b0",
		PortDescription: "Interface  60 as ros0p55",
		Custom: []CustomTLV{{
			OUI:     [3]byte{0x00, 0x0e, 0xab},
			Subtype: 1,
			Info:    []byte(`{"ip_addr":"172.24.0.7/16","ttl":"forever","mtu":9000,"qosProfile":"ll_be_bd_et"}`),
		}},
	}
	if diff := cmp.Diff(want, hsn); diff != "" {
		t.Errorf("hsn0 neighbor (-want +got):\n%s", diff)
	}
	if got[0].PortID != (ID{Type: "ifname", Value: "1/1/47"}) || len(got[0].Custom) != 0 {
		t.Errorf("management neighbor = %+v", got[0])
	}
}

func TestDecodeNeighbors_cn01_keepsForeignOUIs(t *testing.T) {
	got, err := DecodeNeighbors(readFixture(t, "cn01-neighbors.json"))
	if err != nil {
		t.Fatalf("DecodeNeighbors: %v", err)
	}
	mgmt := got[1]
	if mgmt.Interface != "enP1p1s0f0np0" || len(mgmt.Custom) != 1 ||
		mgmt.Custom[0].OUI != [3]byte{0x00, 0x16, 0xb9} || string(mgmt.Custom[0].Info) != "\x00\x01" {
		t.Errorf("management neighbor = %+v", mgmt)
	}
	if len(got[0].Custom) != 1 || len(got[0].Custom[0].Info) != 82 {
		t.Errorf("hsn0 Cray TLV = %+v", got[0].Custom)
	}
}

func TestDecodeNeighbors_returnsNone_whenNoNeighbors(t *testing.T) {
	got, err := DecodeNeighbors(readFixture(t, "empty-neighbors.json"))
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v; want no neighbors", got, err)
	}
}

func TestDecodeNeighbors_skipsNonLLDPEntries(t *testing.T) {
	data := []byte(`{"lldp":[{"interface":[{"name":"eth0","via":"CDPv2","chassis":[{"id":[{"type":"mac","value":"00:11:22:33:44:55"}]}]}]}]}`)
	got, err := DecodeNeighbors(data)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v; want CDP entry skipped", got, err)
	}
}

func TestDecodeNeighbors_acceptsInvalidUTF8InManagementNeighbor(t *testing.T) {
	data := []byte("{\"lldp\":[{\"interface\":[{\"name\":\"eth0\",\"via\":\"LLDP\",\"chassis\":[{\"name\":[{\"value\":\"sw\xff1\"}]}]}]}]}")
	got, err := DecodeNeighbors(data)
	if err != nil || len(got) != 1 {
		t.Errorf("got %v, %v; want one neighbor", got, err)
	}
}

func TestDecodeNeighbors_rejectsLenMismatch(t *testing.T) {
	data := []byte(`{"lldp":[{"interface":[{"name":"hsn0","via":"LLDP","unknown-tlvs":[{"unknown-tlv":[{"oui":"00,0E,AB","subtype":"1","len":"3","value":"7B,7D"}]}]}]}]}`)
	if _, err := DecodeNeighbors(data); err == nil {
		t.Fatal("want error for len/value mismatch")
	}
}

func TestDecodeNeighbors_rejectsMalformedOutput(t *testing.T) {
	if _, err := DecodeNeighbors([]byte(`{"lldp":`)); err == nil {
		t.Fatal("want error for truncated output")
	}
}

func TestDecodeInterfaces_cn01(t *testing.T) {
	got, err := DecodeInterfaces(readFixture(t, "cn01-interfaces.json"))
	if err != nil {
		t.Fatalf("DecodeInterfaces: %v", err)
	}
	if diff := cmp.Diff([]string{"hsn0", "enP1p1s0f0np0", "veth8cfdad97"}, got); diff != "" {
		t.Errorf("interfaces (-want +got):\n%s", diff)
	}
}
