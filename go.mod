module github.com/opencube-horizon/slingshot-netcfg

go 1.27

toolchain go1.27.1

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/google/go-cmp v0.7.0
	github.com/vishvananda/netlink v1.3.1
	go.uber.org/goleak v1.3.0
	golang.org/x/sync v0.23.0
	golang.org/x/sys v0.48.0
)

require github.com/vishvananda/netns v0.0.5 // indirect
