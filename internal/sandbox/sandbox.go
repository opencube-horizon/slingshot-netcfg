// Package sandbox confines the running service with Landlock (spec §6.15).
package sandbox

import (
	"fmt"

	"github.com/landlock-lsm/go-landlock/landlock"
	llsys "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

// Paths are the file system locations the service uses once it runs.
type Paths struct {
	// SysRoot is read for interface selection (§6.2).
	SysRoot string
	// ProcNet holds the per-interface sysctls, read and written (§6.8).
	ProcNet string
	// RunDir receives state.json and the readiness files (§6.9).
	RunDir string
	// LLDPCLI is executed; it inherits the sandbox.
	LLDPCLI string
}

// libDirs hold lldpcli's dynamic loader and shared libraries on the
// distributions (glibc) and on Talos (musl). Missing ones are skipped.
var libDirs = []string{"/lib", "/lib64", "/usr/lib", "/usr/lib64", "/usr/local/lib"}

// Restrict confines this process and its future children to p: no other
// file access, no TCP, no abstract UNIX sockets and no signals outside the
// sandbox. It uses the Landlock features up to ABI 6 that the kernel
// supports, and returns the kernel's Landlock ABI version (0 if Landlock
// is unavailable, in which case nothing is restricted).
func Restrict(p Paths) (int, error) {
	abi, err := llsys.LandlockGetABIVersion()
	if err != nil {
		abi = 0
	}
	err = landlock.V6.BestEffort().Restrict(
		landlock.RODirs(p.SysRoot),
		landlock.RWFiles(p.ProcNet),
		landlock.RWDirs(p.RunDir),
		landlock.ROFiles(p.LLDPCLI),
		landlock.RODirs(libDirs...).IgnoreIfMissing(),
		landlock.ROFiles("/etc/ld.so.cache").IgnoreIfMissing(),
		// os/exec connects lldpcli's stdin to /dev/null.
		landlock.RWFiles("/dev/null"),
	)
	if err != nil {
		return abi, fmt.Errorf("landlock: %w", err)
	}
	return abi, nil
}
