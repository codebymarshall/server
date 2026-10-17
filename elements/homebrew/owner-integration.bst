kind: manual
description: |
  Homebrew integration's owner orchestration, without engine binaries. The
  selected profile provisions the canonical feature selector before fetching;
  this sysext supplies it alongside owner preparation after the merge.
  Homebrew supplies the root-owned UID:GID record. Only selected rootless
  engines start; no global runtime preset or privileged guest endpoint exists.

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
    install -D -m0755 runtime-src/bluefin-oci-owner-prepare runtime-src/bluefin-oci-features -t "%{install-root}%{libexecdir}"
    install -D -m0644 runtime-src/bluefin-oci-owner-prepare.service "$lib/systemd/system/bluefin-oci-owner-prepare.service"
    install -D -m0644 runtime-src/80-bluefin-oci.preset "$lib/systemd/system-preset/80-bluefin-oci.preset"
