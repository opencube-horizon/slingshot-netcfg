# Static binaries:
#
#   make binaries            # dist/slingshot-netcfg-linux-{amd64,arm64} + SHA256SUMS
#
# Checks (tools pinned in tools/go.mod, built with the repo's toolchain):
#
#   make lint                # golangci-lint, config in .golangci.yml
#   make fmt                 # apply gofumpt + goimports
#   make test                # unit tests with the race detector (Linux only)
#   make nilaway             # nil-safety analysis (nilaway)
#
# Diagrams (docs/*.puml → SVG, PNG; PlantUML lays them out with Graphviz):
#
#   nix shell nixpkgs#plantuml -c make diagrams

GO ?= go
ARCHES ?= amd64 arm64
DIST ?= dist
BINARY := slingshot-netcfg

PLANTUML ?= plantuml

DIAGRAMS := $(wildcard docs/*.puml)
SVGS := $(DIAGRAMS:.puml=.svg)
PNGS := $(DIAGRAMS:.puml=.png)

.PHONY: binaries clean lint fmt test nilaway diagrams clean-diagrams

# Same flags as the Talos and RPM builds (spec NFR-1): static, -trimpath.
binaries:
	rm -rf $(DIST) && mkdir -p $(DIST)
	for arch in $(ARCHES); do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath \
			-o $(DIST)/$(BINARY)-linux-$$arch ./cmd/$(BINARY) || exit 1; \
	done
	cd $(DIST) && sha256sum $(BINARY)-linux-* > SHA256SUMS

clean:
	rm -rf $(DIST)

# The tools are built for the host (go tool -n prints the built binary);
# the code is analysed as linux, the only platform it runs on.
GOTOOL := $(GO) tool -modfile=tools/go.mod

lint:
	GOOS=linux "$$($(GOTOOL) -n golangci-lint)" run ./...

fmt:
	GOOS=linux "$$($(GOTOOL) -n golangci-lint)" fmt ./...

test:
	$(GO) test -race -shuffle=on -count=1 ./...

nilaway:
	GOOS=linux "$$($(GOTOOL) -n nilaway)" -include-pkgs=github.com/opencube-horizon/$(BINARY) ./...

diagrams: $(SVGS) $(PNGS)

%.svg: %.puml
	$(PLANTUML) -tsvg $<

%.png: %.puml
	$(PLANTUML) -tpng -Sdpi=150 $<

clean-diagrams:
	rm -f $(SVGS) $(PNGS)
