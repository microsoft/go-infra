%global goroot          %{_libdir}/golang
%global gopath          %{_datadir}/gocode
%global ms_go_filename  go1.23.0-20240708.2.src.tar.gz
%global ms_go_revision  2
%ifarch aarch64
%global gohostarch      arm64
%else
%global gohostarch      amd64
%endif
%define debug_package %{nil}
%define __strip /bin/true
# rpmbuild magic to keep from having meta dependency on libc.so.6
%define _use_internal_dependency_generator 0
%define __find_requires %{nil}
Summary:        Go
Name:           golang
Version:        1.23.0
Release:        1%{?dist}
License:        BSD-3-Clause
Vendor:         Microsoft Corporation
Distribution:   Azure Linux
Group:          System Environment/Security
URL:            https://github.com/microsoft/go
Source0:        https://github.com/microsoft/go/releases/download/v%{version}-%{ms_go_revision}/%{ms_go_filename}

# bootstrap 00, same content as https://dl.google.com/go/go1.4-bootstrap-20171003.tar.gz
Source1:        https://github.com/microsoft/go/releases/download/v1.4.0-1/go1.4-bootstrap-20171003.tar.gz
Patch0:         go14_bootstrap_aarch64.patch
# bootstrap 01
Source2:        https://github.com/microsoft/go/releases/download/v1.19.12-1/go.20230802.5.src.tar.gz
# bootstrap 02
Source3:        https://github.com/microsoft/go/releases/download/v1.20.14-1/go.20240206.2.src.tar.gz

Provides:       %{name} = %{version}
Provides:       go = %{version}-%{release}
Provides:       msft-golang = %{version}-%{release}

%description
Go is an open source programming language that makes it easy to build simple, reliable, and efficient software.

%prep
# Setup bootstrap source
tar xf %{SOURCE1} --no-same-owner
patch -Np1 --ignore-whitespace < %{PATCH0}
mv -v go go-bootstrap-00

tar xf %{SOURCE2} --no-same-owner
mv -v go go-bootstrap-01

tar xf %{SOURCE3} --no-same-owner
mv -v go go-bootstrap-02

%setup -q -n go

%build
# go 1.4 bootstraps with C.
# go 1.20 bootstraps with go >= 1.17.13
# go >= 1.22 bootstraps with go >= 1.20.14
#
# These conditions make building the current go compiler from C a multistep
# process. Approximately once a year, the bootstrap requirement is moved
# forward, adding another step.
#
# PS: Since go compiles fairly quickly, the extra overhead is around 2-3 minutes
#     on a reasonable machine.

# Use prev bootstrap to compile next bootstrap.
function go_bootstrap() {
  local bootstrap=$1
  local new_root=%{_topdir}/BUILD/go-bootstrap-${bootstrap}
  (
    cd ${new_root}/src
    CGO_ENABLED=0 ./make.bash
  )
  # Nuke the older bootstrapper
  rm -rf %{_libdir}/golang
  # Install the new bootstrapper
  mv -v $new_root %{_libdir}/golang
  export GOROOT=%{_libdir}/golang
  export GOROOT_BOOTSTRAP=%{_libdir}/golang
}

go_bootstrap 00
go_bootstrap 01
go_bootstrap 02

# Build current go version
export GOHOSTOS=linux
export GOHOSTARCH=%{gohostarch}
export GOROOT_BOOTSTRAP=%{goroot}

export GOROOT="`pwd`"
export GOPATH=%{gopath}
export GOROOT_FINAL=%{_bindir}/go
rm -f  %{gopath}/src/runtime/*.c
(
  cd src
  ./make.bash --no-clean
)

%install

mkdir -p %{buildroot}%{_bindir}
mkdir -p %{buildroot}%{goroot}

cp -R api bin doc lib pkg src misc VERSION go.env %{buildroot}%{goroot}

# remove the unnecessary zoneinfo file (Go will always use the system one first)
rm -rfv %{buildroot}%{goroot}/lib/time

# remove the doc Makefile
rm -rfv %{buildroot}%{goroot}/doc/Makefile

# put binaries to bindir, linked to the arch we're building,
# leave the arch independent pieces in %{goroot}
mkdir -p %{buildroot}%{goroot}/bin/linux_%{gohostarch}
ln -sfv ../go %{buildroot}%{goroot}/bin/linux_%{gohostarch}/go
ln -sfv ../gofmt %{buildroot}%{goroot}/bin/linux_%{gohostarch}/gofmt
ln -sfv %{goroot}/bin/gofmt %{buildroot}%{_bindir}/gofmt
ln -sfv %{goroot}/bin/go %{buildroot}%{_bindir}/go

# ensure these exist and are owned
mkdir -p %{buildroot}%{gopath}/src/github.com/
mkdir -p %{buildroot}%{gopath}/src/bitbucket.org/
mkdir -p %{buildroot}%{gopath}/src/code.google.com/p/

# This file is not necessary: recent Go toolsets have good defaults.
# Keep the file, but leave it blank. This makes the upgrade path very simple.
install -vdm755 %{buildroot}%{_sysconfdir}/profile.d
cat >> %{buildroot}%{_sysconfdir}/profile.d/go-exports.sh <<- "EOF"
EOF

%post -p /sbin/ldconfig
%postun
/sbin/ldconfig
if [ $1 -eq 0 ]; then
  #This is uninstall
  rm %{_sysconfdir}/profile.d/go-exports.sh
  rm -rf /opt/go
  exit 0
fi

%files
%defattr(-,root,root)
%license LICENSE
%exclude %{goroot}/src/*.rc
%exclude %{goroot}/include/plan9
%{_sysconfdir}/profile.d/go-exports.sh
%{goroot}/*
%{gopath}/src
%exclude %{goroot}/src/pkg/debug/dwarf/testdata
%exclude %{goroot}/src/pkg/debug/elf/testdata
%{_bindir}/*

%changelog
* Tue Jun 04 2024 Davis Goodin <dagood@microsoft.com> - 1.22.4-1
- Bump version to 1.22.4-1

* Tue May 07 2024 Davis Goodin <dagood@microsoft.com> - 1.22.3-1
- Bump version to 1.22.3-1

* Wed May 08 2024 Davis Goodin <dagood@microsoft.com> - 1.21.9-2
- Remove explicit Go env variable defaults
