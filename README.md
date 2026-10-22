# slingshot-netcfg

Provisions HPE Slingshot (Cassini) HSN interfaces `hsnN` from the Cray LLDP TLV
the Rosetta switch advertises, read through lldpd: fabric MAC, IPv4 address and
lifetime, MTU, per-interface sysctls and source-based policy routing. It runs as
a level-triggered reconciler and replaces HPE's `slingshot-network-config`.

Platforms: Talos Linux (system extension), RHEL 10 and openSUSE Leap 16 (RPM);
amd64 and arm64. Linux only.

## Build

```sh
make binaries        # static binaries in dist/ (CGO_ENABLED=0, -trimpath)
make test            # unit tests (Linux)
make lint
scripts/integration-test.sh   # netns integration tests in a privileged Docker container
```

Packaging: `packaging/rpm/slingshot-netcfg.spec` and the unit in
`packaging/files/`. The Talos extension lives in `slingshot-talos`.

## Run

```
slingshot-netcfg run    [--config PATH] [--lldpcli PATH] [--lldpd-socket PATH] [--iface NAME]...
                        [--dry-run] [--once] [--once-timeout DURATION] [--log-level LEVEL]
slingshot-netcfg decode FILE    # target per interface from saved `lldpcli -f json0 show neighbors details`
```

Requirements: lldpd with custom-TLV support handling `hsn*`, and the matching
`lldpcli`. The config file (`/etc/slingshot-netcfg/config.toml`, TOML) is
optional; every key has a default.

Output in `/run/slingshot-netcfg/`:
- `state.json`: phase, neighbor, target and applied configuration per interface;
- `hsnN.ready`: written once `hsnN` is first provisioned in this boot; use it for
  path-based start dependencies.

Stopping or simply restarting the service never deconfigures anything.

## Making services depend on HSN readiness

The service `slingshot-netcfg.service` itself is ready after its first pass, not
once any or all `hsnN` is provisioned, so `After=slingshot-netcfg.service` is not enough.

The RPM ships a path unit and a target per interface instead:

```sh
systemctl enable --now slingshot-netcfg-ready@hsn0.path
```

`slingshot-netcfg-ready@hsn0.target` becomes active once `hsn0.ready` exists,
which in turn gets created as soon as `hsn0` is provisioned the first time.

Make the target want consumers, e.g. LNet over `cxi0`:

```ini
# /etc/systemd/system/slingshot-netcfg-ready@hsn0.target.d/lnet.conf
[Unit]
Wants=lnet.service
```

and prevent consumers from being started before the used HSN is ready,
in case they are being pulled in by other targets (such as the `multi-user.target`).

```ini
# /etc/systemd/system/lnet.service.d/hsn0-ready.conf
[Unit]
ConditionPathExists=/run/slingshot-netcfg/hsn0.ready
```

On Talos, make the extension service depend on the readiness file instead:

```yaml
depends:
  - path: /run/slingshot-netcfg/hsn0.ready
```

## Bring-up

![Bring-up of one Cassini NIC](docs/hsn-bringup.svg)
