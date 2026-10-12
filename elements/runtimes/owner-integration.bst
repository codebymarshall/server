kind: manual
description: |
  Base OS orchestration only, no engine binaries. The existing optional fetch
  imports explicit installer choices before fetching signed runtime sysexts.
  Homebrew preparation supplies the canonical root-owned UID:GID owner record;
  this helper allocates authorized subordinate IDs and starts selected user
  engines with retained private endpoint directories. No global runtime preset,
  privileged guest endpoint, Docker group or broad command permission is granted.

build-depends:
- base/base-stack.bst

sources:
- kind: local
  path: files/runtimes
  directory: runtime-src

variables:
  strip-binaries: ""

config:
  install-commands:
  - |
    set -eu
    lib="%{install-root}%{indep-libdir}"
    install -D -m0755 runtime-src/bluefin-oci-owner-prepare "%{install-root}%{libexecdir}/bluefin-oci-owner-prepare"
    install -D -m0644 -t "$lib/systemd/system" runtime-src/bluefin-oci-owner-prepare.service runtime-src/bluefin-oci-features.service
    install -D -m0644 runtime-src/45-home-server-runtimes.conf "$lib/systemd/system/bluefin-sysext-fetch.service.d/45-home-server-runtimes.conf"
    install -D -m0644 runtime-src/80-bluefin-oci.preset "$lib/systemd/system-preset/80-bluefin-oci.preset"
