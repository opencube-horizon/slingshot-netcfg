// Command slingshot-netcfg provisions Slingshot HSN interfaces from the Cray
// LLDP TLV reported by lldpd and maintains their policy routing.
package main

import (
	"fmt"
	"os"
	"runtime"
)

const usage = `usage:
  slingshot-netcfg run    [--config PATH] [--lldpcli PATH] [--lldpd-socket PATH] [--iface NAME]...
                          [--dry-run] [--once] [--once-timeout DURATION] [--log-level LEVEL]
  slingshot-netcfg decode FILE
`

func main() {
	capGOMAXPROCS(maxProcs)
	os.Exit(run(os.Args[1:]))
}

// maxProcs bounds GOMAXPROCS: the service is mostly idle, and every P widens
// garbage collection and thread fan-out across the node's cores
// (docs/Go-Talos-Jitter.md).
const maxProcs = 2

// capGOMAXPROCS lowers GOMAXPROCS to limit unless the operator set the
// GOMAXPROCS environment variable, which always wins.
func capGOMAXPROCS(limit int) {
	if _, set := os.LookupEnv("GOMAXPROCS"); set {
		return
	}
	if runtime.GOMAXPROCS(0) > limit {
		runtime.GOMAXPROCS(limit)
	}
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "run":
		return runCmd(args[1:])
	case "decode":
		return decodeCmd(args[1:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n%s", args[0], usage)
		return 2
	}
}
