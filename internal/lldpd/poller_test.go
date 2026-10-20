package lldpd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencube-horizon/slingshot-netcfg/internal/config"
)

// fakeLLDPCLI writes an executable lldpcli stand-in running script, and
// returns a poller using it.
func fakeLLDPCLI(t *testing.T, socket, script string) *Poller {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lldpcli")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	p, err := NewPoller(config.LLDPD{LLDPCLI: path, Socket: socket, CommandTimeout: 2 * time.Second, PollInterval: time.Second}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPoll_buildsSnapshotFromBothCommands(t *testing.T) {
	fixtures, _ := filepath.Abs(filepath.Join("..", "..", "testdata", "lldpcli"))
	p := fakeLLDPCLI(t, "", `case "$4" in
neighbors) cat `+fixtures+`/g100n032-neighbors.json ;;
interfaces) cat `+fixtures+`/cn01-interfaces.json ;;
esac
`)

	s, err := p.Poll(t.Context())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if len(s.Neighbors["hsn0"]) != 1 || !s.Handled["hsn0"] || s.Handled["hsn9"] {
		t.Errorf("snapshot = %+v", s)
	}
}

func TestPoll_passesSocketAndFormat(t *testing.T) {
	argv := filepath.Join(t.TempDir(), "argv")
	p := fakeLLDPCLI(t, "/run/x.sock", `echo "$@" >> `+argv+`
echo '{"lldp":[{}]}'
`)

	if _, err := p.Poll(t.Context()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	got, _ := os.ReadFile(argv)
	want := "-u /run/x.sock -f json0 show neighbors details\n-u /run/x.sock -f json0 show interfaces\n"
	if string(got) != want {
		t.Errorf("argv =\n%s\nwant\n%s", got, want)
	}
}

func TestPoll_reportsStderr_whenLLDPCLIFails(t *testing.T) {
	p := fakeLLDPCLI(t, "", "echo 'unable to connect to socket' >&2\nexit 1\n")

	_, err := p.Poll(t.Context())

	if err == nil || !strings.Contains(err.Error(), "unable to connect to socket") {
		t.Errorf("err = %v, want stderr in error", err)
	}
}

func TestPoll_killsLLDPCLI_afterTimeout(t *testing.T) {
	p := fakeLLDPCLI(t, "", "exec sleep 30\n")
	start := time.Now()

	_, err := p.Poll(t.Context())

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Poll took %s, want bounded by timeout + WaitDelay", elapsed)
	}
}
