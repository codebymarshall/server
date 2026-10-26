kind: manual
description: |
  Import the upstream nspawn.org release binary for systemd-nspawn machine management.

(@):
- include/arch.yml
- include/nspawn.yml

build-depends:
- base/base-stack.bst

variables:
  strip-binaries: ""

sources:
- kind: tar
  url: github:nspawn/nspawn/releases/download/%{nspawn-org-version}/nspawn-%{nspawn-org-version}-x86_64-linux.tar.gz
  ref: "%{nspawn-org-x86-64-sha256}"
  base-dir: "nspawn-%{nspawn-org-version}"
  directory: nspawn-dist

config:
  install-commands:
  - |
    install -Dm0755 nspawn-dist/nspawn "%{install-root}%{bindir}/nspawn"
