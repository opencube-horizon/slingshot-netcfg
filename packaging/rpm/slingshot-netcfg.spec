# Go binaries carry no debugsource the RPM tooling can extract.
%global debug_package %{nil}

Name:           slingshot-netcfg
Version:        0.1.2
Release:        1%{?dist}
Summary:        Slingshot HSN provisioning from LLDP
License:        MIT
URL:            https://github.com/opencube-horizon/slingshot-netcfg
Source0:        %{url}/archive/v%{version}/%{name}-%{version}.tar.gz
# Module dependencies for an offline build, created by scripts/vendor-tarball.sh.
Source1:        %{name}-%{version}-vendor.tar.gz

ExclusiveArch:  x86_64 aarch64
BuildRequires:  golang >= 1.27
BuildRequires:  systemd-rpm-macros
Requires:       lldpd
Requires(pre):  systemd
Conflicts:      slingshot-network-config
%{?systemd_requires}

%description
slingshot-netcfg provisions Cassini HSN interfaces (hsnN) from the Cray LLDP
TLV that the Rosetta switch advertises: fabric MAC, IPv4 address and lifetime,
MTU, source-based policy routing and per-interface sysctls. It reads LLDP
neighbors from lldpd and replaces HPE's slingshot-network-config.

%prep
%autosetup -n %{name}-%{version}
tar -xzf %{SOURCE1}

%build
export CGO_ENABLED=0 GOFLAGS=-mod=vendor GOTOOLCHAIN=local
go build -trimpath -ldflags "-B gobuildid" -o %{name} ./cmd/%{name}

%check
export CGO_ENABLED=0 GOFLAGS=-mod=vendor GOTOOLCHAIN=local
go test ./internal/...

%install
install -D -m 0755 %{name} %{buildroot}%{_bindir}/%{name}
install -D -m 0644 packaging/files/%{name}.service %{buildroot}%{_unitdir}/%{name}.service
install -D -m 0644 packaging/files/%{name}.sysusers %{buildroot}%{_sysusersdir}/%{name}.conf
install -D -m 0644 packaging/files/90-%{name}-networkmanager.conf %{buildroot}%{_prefix}/lib/NetworkManager/conf.d/90-%{name}.conf
install -D -m 0644 packaging/files/90-%{name}-sysctl.conf %{buildroot}%{_sysctldir}/90-%{name}.conf
install -D -m 0644 packaging/files/99-%{name}-limits.conf %{buildroot}%{_sysconfdir}/security/limits.d/99-%{name}.conf

# The service user, as in packaging/files/%{name}.sysusers. The payload
# isn't installed yet in %pre, so the line is passed on stdin.
%define create_user \
echo 'u %{name} - "Slingshot HSN provisioning from LLDP" -' | systemd-sysusers --replace=%{_sysusersdir}/%{name}.conf - || :

# On upgrade from a version that ran as root: systemd can't take over the
# preserved runtime directory itself, because SELinux denies init_t the chown.
%define chown_rundir \
if [ -d /run/%{name} ]; then chown -R %{name}:%{name} /run/%{name} || :; fi

# NetworkManager only reads the unmanaged-devices drop-in on reload; until
# then it keeps managing hsnN. A reload doesn't touch existing connections.
%define reload_networkmanager \
if systemctl -q is-active NetworkManager.service 2>/dev/null; then systemctl reload NetworkManager.service || :; fi

%if 0%{?suse_version}
%pre
%create_user
%service_add_pre %{name}.service

%post
%chown_rundir
%reload_networkmanager
%service_add_post %{name}.service
%sysctl_apply 90-%{name}.conf

%preun
%service_del_preun %{name}.service

%postun
%service_del_postun %{name}.service
%else
%pre
%create_user

%post
%chown_rundir
%reload_networkmanager
%systemd_post %{name}.service
%sysctl_apply 90-%{name}.conf

%preun
%systemd_preun %{name}.service

%postun
%systemd_postun_with_restart %{name}.service
%endif

%files
%license LICENSE
%{_bindir}/%{name}
%{_unitdir}/%{name}.service
%{_sysusersdir}/%{name}.conf
%dir %{_prefix}/lib/NetworkManager
%dir %{_prefix}/lib/NetworkManager/conf.d
%{_prefix}/lib/NetworkManager/conf.d/90-%{name}.conf
%{_sysctldir}/90-%{name}.conf
%dir %{_sysconfdir}/security/limits.d
%config(noreplace) %{_sysconfdir}/security/limits.d/99-%{name}.conf

%changelog
* Thu Oct 22 2026 Tiziano Müller <tiziano.mueller@hpe.com> - 0.1.2-1
- Keep Landlock access to /proc/sys/net after dentry eviction
- Report when lldpd hasn't joined the LLDP multicast group

* Wed Oct 21 2026 Tiziano Müller <tiziano.mueller@hpe.com> - 0.1.1-1
- Run unprivileged and sandboxed, with CAP_NET_ADMIN and CAP_NET_RAW;
  sd_notify readiness, status and watchdog; Landlock
- Reload NetworkManager after install
- Own /etc/security/limits.d

* Tue Oct 06 2026 Tiziano Müller <tiziano.mueller@hpe.com> - 0.1.0-1
- Initial package
