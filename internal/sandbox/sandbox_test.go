package sandbox

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	llsys "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

// Landlock can't be lifted once applied, so each check runs in a child
// process (this test binary, re-executed) that restricts itself first.
const childEnv = "SANDBOX_TEST_CHECK"

type dirs struct{ sys, net, run, other string }

func dirsOf(root string) dirs {
	return dirs{sys: filepath.Join(root, "sys"), net: filepath.Join(root, "net"), run: filepath.Join(root, "run"), other: filepath.Join(root, "other")}
}

func errPermission(err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return nil
	}
	return fmt.Errorf("got %w, want permission denied", err)
}

var checks = map[string]func(d dirs) error{
	"writes RunDir":       func(d dirs) error { return os.WriteFile(filepath.Join(d.run, "state.json"), nil, 0o644) },
	"writes ProcNet":      func(d dirs) error { return os.WriteFile(filepath.Join(d.net, "rp_filter"), []byte("2"), 0o644) },
	"reads SysRoot":       func(d dirs) error { _, err := os.ReadFile(filepath.Join(d.sys, "vendor")); return err },
	"runs lldpcli":        func(dirs) error { return exec.Command(lldpcli()).Run() },
	"can't write SysRoot": func(d dirs) error { return errPermission(os.WriteFile(filepath.Join(d.sys, "vendor"), nil, 0o644)) },
	"can't read elsewhere": func(d dirs) error {
		_, err := os.ReadFile(filepath.Join(d.other, "secret"))
		return errPermission(err)
	},
	"can't run other programs": func(dirs) error {
		sh, err := exec.LookPath("sh")
		if err != nil {
			return err
		}
		return errPermission(exec.Command(sh, "-c", "true").Run())
	},
	"can't connect via TCP": func(dirs) error {
		_, err := net.Dial("tcp", "127.0.0.1:9")
		if errors.Is(err, syscall.EACCES) {
			return nil
		}
		return fmt.Errorf("got %w, want EACCES", err)
	},
}

// lldpcli stands in for lldpcli: any dynamically linked program.
func lldpcli() string {
	p, err := exec.LookPath("true")
	if err != nil {
		panic(err)
	}
	return p
}

func TestMain(m *testing.M) {
	if name := os.Getenv(childEnv); name != "" {
		os.Exit(runChild(name))
	}
	os.Exit(m.Run())
}

func runChild(name string) int {
	d := dirsOf(os.Getenv("SANDBOX_TEST_ROOT"))
	if _, err := Restrict(Paths{SysRoot: d.sys, ProcNet: d.net, RunDir: d.run, LLDPCLI: lldpcli()}); err != nil {
		fmt.Fprintln(os.Stderr, "Restrict:", err)
		return 1
	}
	if err := checks[name](d); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func TestRestrict_confinesProcessAndChildren(t *testing.T) {
	if abi, err := llsys.LandlockGetABIVersion(); err != nil || abi < 4 {
		t.Skipf("Landlock ABI %d (%v): file and TCP rules need ABI 4", abi, err)
	}
	root := t.TempDir()
	d := dirsOf(root)
	for _, dir := range []string{d.sys, d.net, d.run, d.other} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{filepath.Join(d.sys, "vendor"), filepath.Join(d.net, "rp_filter"), filepath.Join(d.other, "secret")} {
		if err := os.WriteFile(f, []byte("0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name := range checks {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^$")
			cmd.Env = append(os.Environ(), childEnv+"="+name, "SANDBOX_TEST_ROOT="+root)

			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Errorf("%v: %s", err, out)
			}
		})
	}
}
