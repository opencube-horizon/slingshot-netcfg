//go:build integration

// Package integration runs slingshot-netcfg against real kernel networking
// and real lldpd instances (spec §12.2). It needs root and network
// namespaces; run it via scripts/integration-test.sh.
package integration

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const hostSocket = "/run/lldpd/lldpd.socket"

// peer emulates one Rosetta port: an lldpd in its own namespace on the far
// end of a veth pair, advertising the Cray TLV.
type peer struct {
	ns, host, port, mac string
	socket              string
	cmd                 *exec.Cmd
}

func newPeer(n int, mac string) *peer {
	return &peer{
		ns: fmt.Sprintf("rosetta%d", n), host: fmt.Sprintf("hsn%d", n), port: fmt.Sprintf("r%d", n), mac: mac,
		socket: fmt.Sprintf("/run/lldpd-rosetta%d.socket", n),
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func (p *peer) create(t *testing.T) {
	run(t, "ip", "netns", "add", p.ns)
	run(t, "ip", "link", "add", p.host, "type", "veth", "peer", "name", p.port, "netns", p.ns)
	run(t, "ip", "-n", p.ns, "link", "set", p.port, "address", p.mac)
	run(t, "ip", "-n", p.ns, "link", "set", "lo", "up")
	run(t, "ip", "-n", p.ns, "link", "set", p.port, "up")
	p.start(t)
	t.Cleanup(func() { p.stop(); _ = exec.Command("ip", "netns", "del", p.ns).Run() })
}

func (p *peer) start(t *testing.T) {
	p.cmd = startLLDPD(t, []string{"netns", "exec", p.ns}, p.socket, p.ns, p.port)
	lldpcli(t, p.socket, "configure", "lldp", "tx-interval", "1")
}

func (p *peer) stop() {
	if p.cmd != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
		p.cmd = nil
	}
}

// advertise sets the Cray TLV payload on the peer port.
func (p *peer) advertise(t *testing.T, payload string, replace bool) {
	args := []string{"configure", "ports", p.port, "lldp", "custom-tlv"}
	if replace {
		args = append(args, "replace")
	}
	args = append(args, "oui", "00,0e,ab", "subtype", "1", "oui-info", hexList(payload))
	lldpcli(t, p.socket, args...)
}

func hexList(s string) string {
	parts := make([]string, len(s))
	for i := range len(s) {
		parts[i] = hex.EncodeToString([]byte{s[i]})
	}
	return strings.Join(parts, ",")
}

func lldpcli(t *testing.T, socket string, args ...string) {
	t.Helper()
	run(t, "lldpcli", append([]string{"-u", socket}, args...)...)
}

// startLLDPD starts lldpd in the foreground (optionally via `ip netns exec`)
// and waits until its control socket answers.
func startLLDPD(t *testing.T, prefix []string, socket, name, pattern string) *exec.Cmd {
	t.Helper()
	if err := os.MkdirAll("/run/lldpd", 0o755); err != nil {
		t.Fatal(err)
	}
	args := append(prefix, "lldpd", "-d", "-p", "/run/lldpd-"+name+".pid", "-u", socket, "-I", pattern)
	cmd := exec.Command("ip", args...)
	if len(prefix) == 0 {
		cmd = exec.Command("lldpd", args[1:]...)
	}
	logf, err := os.Create(filepath.Join(t.TempDir(), name+".log"))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lldpd %s: %v", name, err)
	}
	eventually(t, 10*time.Second, func() error {
		return exec.Command("lldpcli", "-u", socket, "show", "configuration").Run()
	})
	return cmd
}

// eventually retries check until it returns nil or timeout passes.
func eventually(t *testing.T, timeout time.Duration, check func() error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var err error
	for {
		if err = check(); err == nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("condition not met within %s: %v", timeout, err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

var errNotYet = errors.New("not yet")
