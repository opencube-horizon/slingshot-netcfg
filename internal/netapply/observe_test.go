package netapply

import (
	"maps"
	"testing"
)

func TestParseLLDPGroup_findsInterfacesWithLLDPAddress(t *testing.T) {
	// /proc/net/dev_mcast of daos10, with hsn0 lacking the group as on cn11.
	in := `2    ens2f0np0       1     0     01005e000001
2    ens2f0np0       1     1     0180c200000e
2    ens2f0np0       1     1     0180c2000003
4    hsn0            1     0     01005e000001
5    hsn1            1     1     0180c200000e
`

	got := parseLLDPGroup(in)

	if want := map[string]bool{"ens2f0np0": true, "hsn1": true}; !maps.Equal(got, want) {
		t.Errorf("parseLLDPGroup = %v, want %v", got, want)
	}
}
