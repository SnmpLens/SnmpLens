# The release's .rpm (SnmpLens-linux-amd64.rpm): the same files the .deb
# installs, at the same paths, taken from the tree the .deb is built from
# (release.yml passes it as `payload`), so the two cannot install different
# things. A file added to the .deb and not listed below fails this build as
# installed-but-unpackaged.
#
# There is no Requires: rpm reads the binary's ELF dependencies itself and
# requires them by soname (libwebkit2gtk-4.1.so.0()(64bit), ...), which every
# rpm distribution resolves, where package names differ between Fedora and
# openSUSE.

Name:           snmplens
Version:        %{snmplens_version}
Release:        1
Summary:        SNMP MIB browser, MIB editor and monitoring
License:        MIT
URL:            https://snmplens.com/
BuildArch:      x86_64

# Built and stripped by `wails build` before rpmbuild runs.
%global debug_package %{nil}
%global __strip /bin/true
%global _build_id_links none

%description
A cross-platform SNMP MIB browser, MIB editor and monitoring desktop
application: dashboards drawn from shareable presets, a trap receiver, and
alerts routed to syslog, a webhook or email.

%install
cp -a %{payload}/usr %{buildroot}/

# Modes are stated rather than taken from the tree, which has whatever umask
# the machine that built it had.
%files
%attr(0755,root,root) /usr/local/bin/snmplens
%attr(0644,root,root) /usr/share/applications/snmplens.desktop
%attr(0644,root,root) /usr/share/icons/hicolor/256x256/apps/snmplens.png

# The system service runs this binary, as with the .deb: an upgrade restarts a
# running service onto the new one, and removing the package stops it and keeps
# it from failing at every boot. $1 is 0 only when the package is going for
# good, not when an upgrade removes the old version.
%post
if [ -d /run/systemd/system ]; then
  systemctl try-restart snmplens.service >/dev/null 2>&1 || :
fi

%preun
if [ "$1" -eq 0 ] && [ -d /run/systemd/system ]; then
  systemctl disable --now snmplens.service >/dev/null 2>&1 || :
fi
