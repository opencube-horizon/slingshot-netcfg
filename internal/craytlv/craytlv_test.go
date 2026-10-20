package craytlv

import (
	"errors"
	"net/netip"
	"testing"
)

func TestParse_acceptsObservedPayloads(t *testing.T) {
	cases := []struct {
		name, info string
		want       Payload
	}{
		{
			name: "g100n032",
			info: `{"ip_addr":"172.24.0.7/16","ttl":"forever","mtu":9000,"qosProfile":"ll_be_bd_et"}`,
			want: Payload{Prefix: netip.MustParsePrefix("172.24.0.7/16"), MTU: 9000, Lifetime: Forever, QoSProfile: "ll_be_bd_et"},
		},
		{
			name: "cn01 whitespace",
			info: `{"ip_addr":"10.115.3.3/24","ttl":"forever","mtu": 9000,"qosProfile":"ll_be_bd_et"}`,
			want: Payload{Prefix: netip.MustParsePrefix("10.115.3.3/24"), MTU: 9000, Lifetime: Forever, QoSProfile: "ll_be_bd_et"},
		},
		{
			name: "legacy mock success, trailing NUL",
			info: "{ \"ip_addr\":\"10.253.0.34/16\",\"ttl\":\"forever\",\"mtu\": 9000}\x00",
			want: Payload{Prefix: netip.MustParsePrefix("10.253.0.34/16"), MTU: 9000, Lifetime: Forever},
		},
		{
			name: "numeric ttl",
			info: `{"ip_addr":"10.0.0.1/8","ttl":3600,"mtu":1500}`,
			want: Payload{Prefix: netip.MustParsePrefix("10.0.0.1/8"), MTU: 1500, Lifetime: 3600},
		},
		{
			name: "decimal string ttl",
			info: `{"ip_addr":"10.0.0.1/32","ttl":"120","mtu":68}`,
			want: Payload{Prefix: netip.MustParsePrefix("10.0.0.1/32"), MTU: 68, Lifetime: 120},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse([]byte(tc.info))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got.Prefix != tc.want.Prefix || got.MTU != tc.want.MTU || got.Lifetime != tc.want.Lifetime || got.QoSProfile != tc.want.QoSProfile {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParse_collectsUnknownKeys(t *testing.T) {
	got, err := Parse([]byte(`{"ip_addr":"10.0.0.1/8","ttl":"forever","mtu":9000,"vni":12}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if v, ok := got.Extra["vni"]; !ok || string(v) != "12" {
		t.Errorf("Extra = %v, want vni=12", got.Extra)
	}
}

func TestParse_rejectsInvalidFields(t *testing.T) {
	cases := map[string]struct{ info, field string }{
		"legacy malformed-oui (no ip_addr)": {`{ "ttl":"forever","mtu": 9000}`, "ip_addr"},
		"ipv6 address":                      {`{"ip_addr":"fd00::1/64","ttl":"forever","mtu":9000}`, "ip_addr"},
		"prefix 0":                          {`{"ip_addr":"10.0.0.1/0","ttl":"forever","mtu":9000}`, "ip_addr"},
		"no prefix":                         {`{"ip_addr":"10.0.0.1","ttl":"forever","mtu":9000}`, "ip_addr"},
		"float mtu":                         {`{"ip_addr":"10.0.0.1/8","ttl":"forever","mtu":9000.5}`, "mtu"},
		"string mtu":                        {`{"ip_addr":"10.0.0.1/8","ttl":"forever","mtu":"9000"}`, "mtu"},
		"mtu too small":                     {`{"ip_addr":"10.0.0.1/8","ttl":"forever","mtu":67}`, "mtu"},
		"mtu too large":                     {`{"ip_addr":"10.0.0.1/8","ttl":"forever","mtu":65536}`, "mtu"},
		"missing mtu":                       {`{"ip_addr":"10.0.0.1/8","ttl":"forever"}`, "mtu"},
		"ttl 0":                             {`{"ip_addr":"10.0.0.1/8","ttl":0,"mtu":9000}`, "ttl"},
		"ttl max":                           {`{"ip_addr":"10.0.0.1/8","ttl":4294967295,"mtu":9000}`, "ttl"},
		"ttl word":                          {`{"ip_addr":"10.0.0.1/8","ttl":"never","mtu":9000}`, "ttl"},
		"ttl bool":                          {`{"ip_addr":"10.0.0.1/8","ttl":true,"mtu":9000}`, "ttl"},
		"missing ttl":                       {`{"ip_addr":"10.0.0.1/8","mtu":9000}`, "ttl"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(tc.info))
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != tc.field {
				t.Errorf("err = %v, want FieldError on %q", err, tc.field)
			}
		})
	}
}

func TestParse_rejectsMalformedJSON(t *testing.T) {
	cases := map[string]string{
		"duplicate key": `{"ip_addr":"10.0.0.1/8","ip_addr":"10.0.0.2/8","ttl":"forever","mtu":9000}`,
		"invalid UTF-8": "{\"ip_addr\":\"10.0.0.1/8\",\"ttl\":\"forever\",\"mtu\":9000,\"x\":\"\xff\"}",
		"not an object": `[1,2]`,
		"truncated":     `{"ip_addr":"10.0.0.1/8"`,
	}
	for name, info := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(info)); !errors.Is(err, ErrMalformed) {
				t.Errorf("err = %v, want ErrMalformed", err)
			}
		})
	}
}
