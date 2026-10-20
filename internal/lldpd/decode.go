// Package lldpd reads LLDP neighbor information from lldpd by running
// lldpcli (spec §6.3) and decodes its json0 output.
package lldpd

import (
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Neighbor is one LLDP neighbor as reported by lldpd.
type Neighbor struct {
	Interface       string
	ChassisID       ID
	PortID          ID
	SysName         string
	PortDescription string
	Custom          []CustomTLV
}

// ID is a chassis or port ID with its lldpd subtype name ("mac", "ifname", …).
type ID struct {
	Type, Value string
}

// CustomTLV is an organizationally specific TLV that lldpd didn't recognize.
type CustomTLV struct {
	OUI     [3]byte
	Subtype int
	Info    []byte
}

// ErrDecode reports lldpcli output that doesn't match the json0 shape.
var ErrDecode = errors.New("decode lldpcli output")

type value struct {
	Value string `json:"value"`
}

type id struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type unknownTLV struct {
	OUI     string `json:"oui"`
	Subtype string `json:"subtype"`
	Len     string `json:"len"`
	Value   string `json:"value"`
}

type wireInterface struct {
	Name    string `json:"name"`
	Via     string `json:"via"`
	Chassis []struct {
		ID   []id    `json:"id"`
		Name []value `json:"name"`
	} `json:"chassis"`
	Port []struct {
		ID    []id    `json:"id"`
		Descr []value `json:"descr"`
	} `json:"port"`
	UnknownTLVs []struct {
		UnknownTLV []unknownTLV `json:"unknown-tlv"`
	} `json:"unknown-tlvs"`
	Status []value `json:"status"`
}

type wireDoc struct {
	LLDP []struct {
		Interface []wireInterface `json:"interface"`
	} `json:"lldp"`
}

func decodeDoc(data []byte) ([]wireInterface, error) {
	var doc wireDoc
	// A non-UTF-8 byte in some management switch's description must not
	// make the whole snapshot undecodable.
	if err := json.Unmarshal(data, &doc, jsontext.AllowInvalidUTF8(true)); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDecode, err)
	}
	var out []wireInterface
	for _, l := range doc.LLDP {
		out = append(out, l.Interface...)
	}
	return out, nil
}

func first[T any](s []T) (T, bool) {
	var zero T
	if len(s) == 0 {
		return zero, false
	}
	return s[0], true
}

// DecodeNeighbors decodes `lldpcli -f json0 show neighbors details`.
// Entries learned via protocols other than LLDP are skipped.
func DecodeNeighbors(data []byte) ([]Neighbor, error) {
	ifaces, err := decodeDoc(data)
	if err != nil {
		return nil, err
	}
	var out []Neighbor
	for _, wi := range ifaces {
		if wi.Via != "LLDP" {
			continue
		}
		n, err := neighborOf(wi)
		if err != nil {
			return nil, fmt.Errorf("%w: interface %s: %w", ErrDecode, wi.Name, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func neighborOf(wi wireInterface) (Neighbor, error) {
	n := Neighbor{Interface: wi.Name}
	if ch, ok := first(wi.Chassis); ok {
		n.ChassisID = ID(firstOr(ch.ID))
		n.SysName = firstOr(ch.Name).Value
	}
	if p, ok := first(wi.Port); ok {
		n.PortID = ID(firstOr(p.ID))
		n.PortDescription = firstOr(p.Descr).Value
	}
	if u, ok := first(wi.UnknownTLVs); ok {
		for _, t := range u.UnknownTLV {
			c, err := decodeCustom(t)
			if err != nil {
				return Neighbor{}, err
			}
			n.Custom = append(n.Custom, c)
		}
	}
	return n, nil
}

// firstOr returns the first element, or the zero value of an empty list.
func firstOr[T any](s []T) T {
	v, _ := first(s)
	return v
}

// DecodeInterfaces decodes `lldpcli -f json0 show interfaces` into the names
// of the interfaces lldpd handles.
func DecodeInterfaces(data []byte) ([]string, error) {
	ifaces, err := decodeDoc(data)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(ifaces))
	for _, wi := range ifaces {
		names = append(names, wi.Name)
	}
	return names, nil
}

func decodeCustom(t unknownTLV) (CustomTLV, error) {
	oui, err := parseHexList(t.OUI)
	if err != nil || len(oui) != 3 {
		return CustomTLV{}, fmt.Errorf("unknown TLV OUI %q", t.OUI)
	}
	subtype, err := strconv.Atoi(t.Subtype)
	if err != nil {
		return CustomTLV{}, fmt.Errorf("unknown TLV subtype %q", t.Subtype)
	}
	info, err := parseHexList(t.Value)
	if err != nil {
		return CustomTLV{}, fmt.Errorf("unknown TLV value: %w", err)
	}
	length, err := strconv.Atoi(t.Len)
	if err != nil || length != len(info) {
		return CustomTLV{}, fmt.Errorf("unknown TLV len %q, decoded %d bytes", t.Len, len(info))
	}
	return CustomTLV{OUI: [3]byte(oui), Subtype: subtype, Info: info}, nil
}

// parseHexList parses lldpcli's comma-separated hex bytes ("7B,22,…", case-insensitive).
func parseHexList(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}
	return hex.DecodeString(strings.ReplaceAll(s, ",", ""))
}
