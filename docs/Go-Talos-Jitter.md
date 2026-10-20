# Go helpers on low-jitter HPC nodes: Go runtime, Linux, Talos, Kubernetes

How Go-based node software interferes with compute cores on large HPC nodes,
and how to confine it. This covers small helpers such as slingshot-netcfg,
and the Go-based infrastructure on a node (Talos machined, containerd,
kubelet).

Sources and versions:

| Area | Source | Checked |
|---|---|---|
| Go runtime | go1.27.1 source tree (`src/runtime`, commit `862c888e`) | read in source |
| Talos | `siderolabs/talos` main at `1227b2b` (2026-10-07); tags v1.14.2, v1.15.0-alpha.0 | read in source |
| Kubernetes | kubernetes.io docs (current: v1.37), KEP-4540 | from docs; not checked in source |
| Measurements | daos10 (g100n032, Rocky 10.2, 64 CPUs, Xeon Gold 6338) | measured |

## Summary

- On a large node the problem is not a helper's CPU or memory use. It is
  **which cores its threads wake up on**.
- **Confine first.** Put all node software on a few housekeeping cores with
  CPU affinity or cpusets. GOMAXPROCS alone doesn't keep a process off cores.
- **Then size.** Within its confinement, a Go program sizes GOMAXPROCS to its
  affinity mask on its own. Small helpers should additionally cap GOMAXPROCS
  (slingshot-netcfg: 2).
- Talos v1.14 can only confine through kernel arguments (`isolcpus`).
  `CPUPartitionConfig`, which assigns cpusets per Talos cgroup root, is on
  Talos main but not in any release yet.
- In Kubernetes, `reservedSystemCPUs` doesn't pin anything, and pods outside
  the Guaranteed integer-CPU class share all non-reserved cores, including
  isolated ones.

Notation below: H = housekeeping cores, C = compute cores.

## 1. Go runtime

### GOMAXPROCS

- **Default:** the number of CPUs in the process's affinity mask
  (`sched_getaffinity`), lowered to the cgroup v2 CPU limit (`cpu.max`) if
  one is set. The limit is rounded up, with a minimum of 2
  (`runtime/cgroup_linux.go:109-119`).
- **Updates:** sysmon re-evaluates the default at most once per second
  (`runtime/proc.go:6637-6641`), including the affinity mask. So a process
  whose affinity or cgroup limit changes follows it.
- **Turning updates off:** `GODEBUG=updatemaxprocs=0` disables the updates;
  `GODEBUG=containermaxprocs=0` ignores the cgroup limit.
- An explicit `GOMAXPROCS` environment variable or a call to
  `runtime.GOMAXPROCS(n)` also disables the updates.
- **Isolation flags:** the runtime knows nothing about `isolcpus` or
  `nohz_full`. It only sees the affinity mask (§2 explains why `isolcpus`
  still works).

### OS threads

- New threads (Ms) are created when a goroutine is runnable and all existing
  threads are busy, blocked in syscalls or cgo, or locked with
  `LockOSThread` (`runtime/debug/garbage.go:128-130`).
- **Idle threads are never destroyed.** They're parked and kept. The thread
  count only grows, up to the 10,000 limit of `debug.SetMaxThreads`.
- **The kernel decides where threads run.** A thread blocked in a syscall
  (netlink receive, `fork`/`exec`, `wait`) runs wherever the scheduler puts
  it. GOMAXPROCS limits how many threads run Go code at once, not which CPUs
  they use.

Measured on daos10 (64 CPUs, no isolation), slingshot-netcfg before the
GOMAXPROCS cap:

| Process | GOMAXPROCS | Threads | Distinct CPUs used | RSS |
|---|---|---|---|---|
| service, after 75 min | 64 (default) | 24 | 22 | 23 MB |
| fresh, after 30 s | 64 (default) | 9 | 8 | 10.7 MB |
| fresh, after 30 s | 2 | 8 | 7 | 10.6 MB |
| fresh, after 30 s | 1 | 6 | 6 | 10.6 MB |

The service also showed 1.2 GB VIRT. Almost all of it was `PROT_NONE`
reservations of the Go runtime: address space with no memory behind it, and
present in every Go binary.

### Garbage collection

- **Dedicated mark workers:** round(0.25 × GOMAXPROCS) per GC cycle, with a
  fractional worker where rounding is off by more than 30%
  (`runtime/mgcpacer.go:402-418`).
- **Idle mark workers:** up to GOMAXPROCS − dedicated workers may run on
  otherwise idle Ps (`runtime/mgcpacer.go:450`). They're switched off for the
  periodic forced GC (`mgcpacer.go:433-446`).
- **Forced GC:** an otherwise idle program still runs a GC every 2 minutes
  (`forcegcperiod`, `runtime/proc.go:6527`).
- **What that means at GOMAXPROCS=180:** each forced cycle wakes about 45
  dedicated workers, i.e. 45 threads, for a 20 MB heap. A cycle triggered by
  allocation can add idle workers on up to 180 Ps. At GOMAXPROCS=2, the same
  cycle uses a single fractional worker.
- **Scavenger:** returns memory to the OS, soft-capped at about 1% of CPU
  time (`runtime/mgcscavenge.go`, `scavengePercent = 1`).
- **GOGC / GOMEMLIMIT** change how often allocation triggers a GC. They don't
  affect the 2-minute forced GC, and for daemons of a few MB they barely
  matter.

### Other wakeups

- **sysmon:** sleeps 20 µs at first, doubling up to 10 ms. Once all Ps are
  idle it sleeps until the next timer, at most 60 s (`forcegcperiod / 2`)
  (`runtime/proc.go:6548-6607`).
- **What remains on an idle program:** its own timers (slingshot-netcfg:
  lldpd poll and resync), the forced GC, and events it waits for (netlink).

## 2. Linux: confine, then size

### Kernel isolation

```
isolcpus=managed_irq,domain,<C> nohz_full=<C> rcu_nocbs=<C> irqaffinity=<H>
```

- **`isolcpus` (domain):** removes C from scheduler load balancing.
  - The kernel also starts PID 1 with an affinity mask of only the
    housekeeping CPUs (`sched_init_smp()` in `kernel/sched/core.c`, using
    `housekeeping_cpumask(HK_TYPE_DOMAIN)`). Every process inherits that
    mask.
  - So all daemons, and the default GOMAXPROCS of every Go program, end up
    sized to H without further configuration.
- **`nohz_full` / `rcu_nocbs`:** remove the scheduler tick and RCU callbacks
  from C. `nohz_full` alone doesn't change any affinity mask.
- **`irqaffinity` / `managed_irq`:** keep device interrupts off C as far as
  the drivers allow.
- **Caveat:** C no longer has load balancing. Anything given a cpuset that
  spans C without pinning its threads piles up on a few cores. That's fine
  for MPI ranks pinned by Slurm, and wrong for Kubernetes shared-pool pods
  (§4).

### Without `isolcpus`: cpusets and affinity

- **systemd:**
  - `systemd.cpu_affinity=<H>` on the kernel command line, or
    `CPUAffinity=<H>` in `/etc/systemd/system.conf`, sets the affinity of all
    services.
  - `AllowedCPUs=<H>` on `system.slice` confines them by cgroup cpuset.
  - Both reach Go's affinity-based GOMAXPROCS.
- **Slurm:** core specialization (`CpuSpecList` / `CoreSpecCount`) reserves
  cores for system daemons, including slurmd.
- **cgroup v2:** cpuset partitions (`cpuset.cpus.partition`) can mark C as an
  isolated partition without boot-time `isolcpus`.

### Checking the result

- **Interference on C:** `rtla osnoise` / `rtla timerlat` (kernel `osnoise`
  tracer).
- **Where a process's threads ran:** field 39 of `/proc/<pid>/task/*/stat`.
- **Interrupt spread:** `/proc/interrupts`.

## 3. Talos

### Go runtime of Talos services

- **Capped by Talos:**

  | Service | GOMAXPROCS cap | Source |
  |---|---|---|
  | machined | 4 | `MachinedMaxProcs`, `pkg/machinery/constants/constants.go:1334-1343`; applied in `internal/app/machined/main.go:169-170` |
  | apid | 2 | same constants |
  | trustd | 2 | same constants |
  | dashboard | 2 | same constants |

- **Not capped:** no GOMAXPROCS cap found for containerd, kubelet or etcd.
  They size themselves to whatever affinity mask or cgroup limit they see.
- **Extension services:** `ExtensionServiceConfig` accepts `environment`
  (`pkg/machinery/config/types/runtime/extensions/service_config.go:62`), so
  operators can set `GOMAXPROCS`, `GOGC` etc. per extension service without
  rebuilding it.

### Talos v1.14: kernel arguments only

- Set `isolcpus`, `nohz_full`, `rcu_nocbs` and `irqaffinity` through an
  Image Factory schematic (`customization.extraKernelArgs`), or through
  `machine.install.extraKernelArgs`. With Secure Boot (UKI), the machine
  config's kernel arguments don't apply, so it has to be Image Factory.
- machined is PID 1. With `isolcpus`, every Talos service and extension
  service therefore inherits the housekeeping mask (§2).

### Talos main: `CPUPartitionConfig` (unreleased)

- **Status:** a `CPUPartitionConfig` document exists on Talos main, with
  `internal/app/machined/pkg/controllers/runtime/internal/cpupartition/` as
  its controller. It is not in v1.14.2 or v1.15.0-alpha.0, and its docs URL
  returns 404. Expect it in v1.15.
- **What it does:** it sets `cpuset.cpus` per Talos cgroup root. Shape, taken
  from its test data:

  ```yaml
  apiVersion: v1alpha1
  kind: CPUPartitionConfig
  init:            { cpus: 0-3 }     # machined, early boot
  system:          { cpus: 0-3 }     # apid, trustd, udevd, extension services
  podruntime:      { cpus: 0-3 }     # containerd, kubelet, etcd
  taloscontainers: { cpus: 0-3 }
  kubepods:        { cpus: 4-179 }
  # virtualMachines: { cpus: …, slices: [{ name, cpus, exclusive }] }
  ```

- **Why it's better than `isolcpus`:** compute cores keep load balancing, and
  containerd shims (which run under `podruntime`) stay on housekeeping cores.
- **Re-check on release:** whether its controller sets the kubelet's reserved
  CPUs automatically. A field for that exists in the controller's plan
  (`plan.go`), but I haven't verified the behaviour.

## 4. Kubernetes

Taken from kubernetes.io and KEP-4540; not checked in source.

- **Static CPU manager:** only Guaranteed pods with integer CPU requests get
  exclusive cores. BestEffort and Burstable pods, and pods with fractional
  requests, share the pool of all non-reserved CPUs.
- **`reservedSystemCPUs`** only stops the kubelet from allocating those CPUs
  to pods. It doesn't pin kubelet, containerd or anything else; that takes
  OS-level confinement (§2, §3).
- **`strict-cpu-reservation`** (CPU manager option, KEP-4540) removes the
  reserved CPUs from the shared pool too, so no pod of any QoS class runs on
  them.
- **The CPU manager doesn't know about `isolcpus`.** Isolated CPUs stay in the
  shared pool. Together with §2's caveat, shared-pool pods on an `isolcpus`
  node land on cores without load balancing.
  - Either run only Guaranteed integer-CPU pods on C,
  - or use cpusets (`CPUPartitionConfig` / `nohz_full`) instead of
    `isolcpus`.
- **containerd shims** (`containerd-shim-runc-v2`) run in containerd's
  cgroup, not the pod's. They follow containerd's confinement.
- **Go in pods:**
  - Guaranteed integer-CPU pods size GOMAXPROCS to their exclusive cpuset.
  - Pods with a CPU limit size it to the limit (rounded up, minimum 2).
  - BestEffort pods size it to the whole shared pool.
- **Reference pattern:** OpenShift's `PerformanceProfile` combines reserved
  and isolated CPU sets, kernel arguments, `systemd.cpu_affinity`, IRQ
  steering and kubelet settings in one object.

## 5. Checklist for an HPC node

1. Choose H: a few cores per NUMA node, both SMT siblings, ideally close to
   the NICs. Everything else is C.
2. Confine all node software to H:
   - Talos v1.15+: `CPUPartitionConfig`;
   - Talos v1.14: `isolcpus` and friends;
   - distributions: `systemd.cpu_affinity` / `AllowedCPUs` or Slurm core
     specialization.
3. Keep interrupts on H: `irqaffinity`, `managed_irq`, and driver-specific
   settings.
4. Let Go size itself to H (the default). Cap small helpers explicitly
   (GOMAXPROCS 1–2) to limit thread growth and GC fan-out.
5. Kubernetes: static CPU manager, `reservedSystemCPUs` = H,
   `strict-cpu-reservation`, and only Guaranteed integer-CPU pods for compute
   work.
6. Measure on C with `rtla osnoise` before and after.

## 6. slingshot-netcfg

- **Cap:** the binary caps GOMAXPROCS at 2 at startup, unless the
  `GOMAXPROCS` environment variable is set, which always wins
  (`cmd/slingshot-netcfg/main.go`).
  - 2 rather than 1 matches Go's own floor for cgroup limits, so the GC can
    run alongside the service's work.
  - With the cap, a forced GC uses one fractional worker instead of about 45
    dedicated workers on a 180-core node.
- **Logging:** the effective value is logged at startup (`gomaxprocs=` on the
  `starting` line).
- **Verified on daos10:**

  | GOMAXPROCS env | Effective GOMAXPROCS | Threads after 30 s |
  |---|---|---|
  | unset | 2 | 7 |
  | 8 | 8 | 9 |

- **Placement is left to the platform:** neither the unit nor the Talos
  service hard-codes a CPU list (§5, step 2).
- **cxi_rh** is C, not Go, but follows the same confinement.
