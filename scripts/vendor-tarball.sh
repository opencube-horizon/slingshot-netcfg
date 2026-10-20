#!/bin/sh
# Creates slingshot-netcfg-<version>-vendor.tar.gz (the RPM's Source1) with
# the vendored module dependencies, so the RPM builds without network access.
set -eu
cd "$(dirname "$0")/.."
version=${1:?usage: $0 VERSION}
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
go mod vendor -o "$tmp/vendor"
tar -czf "slingshot-netcfg-$version-vendor.tar.gz" -C "$tmp" vendor
echo "slingshot-netcfg-$version-vendor.tar.gz"
