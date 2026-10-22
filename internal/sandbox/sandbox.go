// Package sandbox confines the running service with Landlock (spec §6.15).
package sandbox

import (
	"fmt"
	"os"

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

// pinned holds the procfs directories that carry rules open for the life of
// the process. When procfs re-creates an evicted dentry, the directory gets a
// new inode without the rule, and access is lost (seen on cn11 after
// drop_caches, for /proc/self/net and /proc/sys/net); an open file pins the
// dentry. Entries below /proc/<pid>/net get a new inode on every lookup, so
// only their directory can carry a rule.
var pinned []*os.File

// Restrict confines this process and its future children to p: no other
// file access (except reading /proc/self/net), no TCP, no abstract UNIX
// sockets and no signals outside the sandbox. It uses the Landlock features
// up to ABI 6 that the kernel supports, and returns the kernel's Landlock ABI
// version (0 if Landlock is unavailable, in which case nothing is restricted).
func Restrict(p Paths) (int, error) {
	abi, err := llsys.LandlockGetABIVersion()
	if err != nil {
		abi = 0
	}
	for _, dir := range []string{"/proc/self/net", p.ProcNet} {
		f, err := os.Open(dir)
		if err != nil {
			return abi, fmt.Errorf("landlock: %w", err)
		}
		pinned = append(pinned, f)
	}
	err = landlock.V6.BestEffort().Restrict(
		landlock.RODirs(p.SysRoot),
		landlock.RWFiles(p.ProcNet),
		landlock.ROFiles("/proc/self/net"),
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
