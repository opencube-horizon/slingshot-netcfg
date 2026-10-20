#!/bin/sh
# Runs the root/netns integration tests (spec §12.2) in a privileged container,
# which provides its own network namespace, lldpd and iproute2.
set -eu
cd "$(dirname "$0")/.."
image=slingshot-netcfg-integration
docker build -q -t "$image" integration >/dev/null
exec docker run --rm --privileged \
	-v "$PWD":/src -w /src \
	-v slingshot-netcfg-gomod:/go/pkg/mod -v slingshot-netcfg-gocache:/root/.cache/go-build \
	"$image" go test -tags integration -race -count=1 -v "$@" ./integration/
