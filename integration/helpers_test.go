//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"github.com/opencube-horizon/slingshot-netcfg/internal/config"
	"github.com/opencube-horizon/slingshot-netcfg/internal/desired"
	"github.com/opencube-horizon/slingshot-netcfg/internal/service"
)

const protocol = 205

type svc struct {
	cancel context.CancelFunc
	done   chan error
}

func startService(t *testing.T, runDir, sysRoot string) *svc {
	t.Helper()
	cfg := config.Default()
	cfg.ResyncInterval = 5 * time.Second
	cfg.LLDPD.PollInterval = time.Second
	cfg.LLDPD.Socket = hostSocket
	out := io.Discard
	if testing.Verbose() {
		out = os.Stderr
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &svc{cancel: cancel, done: make(chan error, 1)}
	go func() {
		s.done <- service.Run(ctx, service.Options{
			Config:  cfg,
			Log:     slog.New(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug})),
			SysRoot: sysRoot,
			RunDir:  runDir,
		})
	}()
	return s
}

func (s *svc) stop(t *testing.T) {
	s.cancel()
	if err := <-s.done; err != nil {
		t.Errorf("service.Run: %v", err)
	}
}

func payload(addr, ttl string) string {
	return fmt.Sprintf(`{"ip_addr":"%s","ttl":%s,"mtu":9000,"qosProfile":"ll_be_bd_et"}`, addr, ttl)
}

func addrs(t *testing.T, ifname string, family int) []netlink.Addr {
	t.Helper()
	l, err := netlink.LinkByName(ifname)
	if err != nil {
		t.Fatal(err)
	}
	a, err := netlink.AddrList(l, family)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func hasAddr(t *testing.T, ifname, prefix string) bool {
	for _, a := range addrs(t, ifname, netlink.FAMILY_V4) {
		if a.IPNet.String() == prefix {
			return true
		}
	}
	return false
}

func taggedRules(t *testing.T) []desired.Rule {
	rules, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	var out []desired.Rule
	for _, r := range rules {
		if r.Protocol != protocol {
			continue
		}
		dr := desired.Rule{Priority: r.Priority, IIF: r.IifName, Table: r.Table, Src: prefixOf(r.Src)}
		if r.Dst != nil {
			dr.Dst = prefixOf(r.Dst)
		}
		out = append(out, dr)
	}
	return out
}

func prefixOf(n *net.IPNet) netip.Prefix {
	a, _ := netip.AddrFromSlice(n.IP)
	bits, _ := n.Mask.Size()
	return netip.PrefixFrom(a.Unmap(), bits)
}

func sameSet[T comparable](a, b []T) bool {
	return len(a) == len(b) && !slices.ContainsFunc(a, func(x T) bool { return !slices.Contains(b, x) })
}

func phase(runDir, ifname string) string {
	b, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		return ""
	}
	i := strings.Index(string(b), `"ifname": "`+ifname+`"`)
	if i < 0 {
		return ""
	}
	rest := string(b[i:])
	j := strings.Index(rest, `"phase": "`)
	if j < 0 {
		return ""
	}
	rest = rest[j+len(`"phase": "`):]
	return rest[:strings.IndexByte(rest, '"')]
}

// cassiniSysfs builds a sysfs tree in which each named interface is a
// Cassini NIC, so the production selector accepts the veth test interfaces.
func cassiniSysfs(t *testing.T, ifnames ...string) string {
	t.Helper()
	root := t.TempDir()
	for i, ifname := range ifnames {
		pci := filepath.Join(root, "devices", "pci0003:00", fmt.Sprintf("0003:%02x:00.0", i+1))
		netDir := filepath.Join(root, "class", "net", ifname)
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
	}
	return root
}
