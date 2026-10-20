// Package craytlv parses the payload of the Cray LLDP TLV (OUI 00:0e:ab,
// subtype 1): UTF-8 JSON carrying the HSN address, MTU and address lifetime
// (spec §5).
package craytlv

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"strconv"
	"strings"
)

// Lifetime is an address lifetime in seconds. Forever is the kernel's
// "infinite" value.
type Lifetime uint32

// Forever is the lifetime of addresses that never expire.
const Forever Lifetime = math.MaxUint32

func (l Lifetime) String() string {
	if l == Forever {
		return "forever"
	}
	return strconv.FormatUint(uint64(l), 10)
}

// Payload is a validated Cray TLV payload.
type Payload struct {
	Prefix     netip.Prefix
	MTU        int
	Lifetime   Lifetime
	QoSProfile string
	// Extra holds keys that are recorded but not applied.
	Extra map[string]jsontext.Value
}

// FieldError reports an invalid or missing payload field.
type FieldError struct {
	Field  string
	Value  string
	Reason string
}

func (e *FieldError) Error() string {
	if e.Value == "" {
		return fmt.Sprintf("Cray TLV field %q: %s", e.Field, e.Reason)
	}
	return fmt.Sprintf("Cray TLV field %q = %s: %s", e.Field, e.Value, e.Reason)
}

// ErrMalformed reports a payload that is not a valid JSON object
// (including invalid UTF-8 and duplicate keys).
var ErrMalformed = errors.New("malformed Cray TLV payload")

type wire struct {
	IPAddr     *string                   `json:"ip_addr"`
	MTU        *int                      `json:"mtu"`
	TTL        *wireTTL                  `json:"ttl"`
	QoSProfile *string                   `json:"qosProfile"`
	Extra      map[string]jsontext.Value `json:",embed"`
}

// Parse validates the TLV info string.
func Parse(info []byte) (Payload, error) {
	info = bytes.TrimRight(info, "\x00 \t\r\n")
	var w wire
	if err := json.Unmarshal(info, &w); err != nil {
		if fe := fieldErrorOf(err); fe != nil {
			return Payload{}, fe
		}
		return Payload{}, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if w.IPAddr == nil {
		return Payload{}, &FieldError{Field: "ip_addr", Reason: "missing"}
	}
	prefix, err := netip.ParsePrefix(*w.IPAddr)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() < 1 {
		return Payload{}, &FieldError{Field: "ip_addr", Value: strconv.Quote(*w.IPAddr), Reason: "want IPv4 CIDR a.b.c.d/p with p in 1..32"}
	}
	if w.MTU == nil {
		return Payload{}, &FieldError{Field: "mtu", Reason: "missing"}
	}
	if *w.MTU < 68 || *w.MTU > 65535 {
		return Payload{}, &FieldError{Field: "mtu", Value: strconv.Itoa(*w.MTU), Reason: "want 68..65535"}
	}
	if w.TTL == nil {
		return Payload{}, &FieldError{Field: "ttl", Reason: "missing"}
	}
	p := Payload{Prefix: prefix, MTU: *w.MTU, Lifetime: w.TTL.l, Extra: w.Extra}
	if w.QoSProfile != nil {
		p.QoSProfile = *w.QoSProfile
	}
	return p, nil
}

func fieldErrorOf(err error) *FieldError {
	var fe *FieldError
	if errors.As(err, &fe) {
		return fe
	}
	var se *json.SemanticError
	if !errors.As(err, &se) {
		return nil
	}
	field := strings.TrimPrefix(string(se.JSONPointer), "/")
	switch field {
	case "ip_addr", "mtu", "ttl", "qosProfile":
		return &FieldError{Field: field, Value: string(se.JSONValue), Reason: "wrong JSON type"}
	default:
		return nil
	}
}

type wireTTL struct{ l Lifetime }

// UnmarshalJSONFrom accepts "forever", an integer, or a decimal string.
func (t *wireTTL) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	var raw string
	switch tok.Kind() {
	case '"':
		raw = tok.String()
		if raw == "forever" {
			t.l = Forever
			return nil
		}
	case '0':
		raw = tok.String()
	default:
		return &FieldError{Field: "ttl", Value: tok.String(), Reason: `want "forever", an integer or a decimal string`}
	}
	secs, err := strconv.ParseUint(raw, 10, 32)
	if err != nil || secs < 1 || secs >= uint64(Forever) {
		return &FieldError{Field: "ttl", Value: strconv.Quote(raw), Reason: "want 1..4294967294 seconds"}
	}
	t.l = Lifetime(secs)
	return nil
}
