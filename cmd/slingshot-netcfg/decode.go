package main

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"

	"github.com/opencube-horizon/slingshot-netcfg/internal/config"
	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
	"github.com/opencube-horizon/slingshot-netcfg/internal/lldpd"
)

type decoded struct {
	Ifname  string `json:"ifname"`
	SysName string `json:"sysName,omitempty"`
	MAC     string `json:"mac,omitempty"`
	Addr    string `json:"addr,omitempty"`
	MTU     int    `json:"mtu,omitzero"`
	TTL     string `json:"ttl,omitempty"`
	QoS     string `json:"qosProfile,omitempty"`
	Error   string `json:"error,omitempty"`
}

// decodeCmd prints the target per interface of a saved
// `lldpcli -f json0 show neighbors details` output. It exits 0 if at least
// one interface yields a valid target.
func decodeCmd(args []string) int {
	if len(args) != 1 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	data, err := os.ReadFile(args[0]) //nolint:gosec // G703: the operator names the file to decode.
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	neighbors, err := lldpd.DecodeNeighbors(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cfg := config.Default()
	byIface := map[string][]lldpd.Neighbor{}
	var order []string
	for _, n := range neighbors {
		if _, ok := byIface[n.Interface]; !ok {
			order = append(order, n.Interface)
		}
		byIface[n.Interface] = append(byIface[n.Interface], n)
	}
	out := make([]decoded, 0, len(order))
	valid := false
	for _, name := range order {
		d := decoded{Ifname: name}
		t, err := desired.DeriveTarget(byIface[name], cfg.OrgTLV, cfg.MAC)
		if err != nil {
			d.Error = err.Error()
		} else {
			valid = true
			d.SysName, d.MAC, d.Addr, d.MTU = t.SysName, t.MAC.String(), t.Payload.Prefix.String(), t.Payload.MTU
			d.TTL, d.QoS = t.Payload.Lifetime.String(), t.Payload.QoSProfile
		}
		out = append(out, d)
	}
	b, err := json.Marshal(out, jsontext.Multiline(true))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(string(b))
	if !valid {
		return 1
	}
	return 0
}
