kind: manual
description: |
  Opt-in systemd-sysext with the nspawn.org CLI tool for container/machine management.

(@):
- include/arch.yml
- include/image.yml
- include/nspawn.yml
- include/sysext.yml

build-depends:
- base/base-stack.bst
- freedesktop-sdk.bst:components/erofs-utils.bst
- freedesktop-sdk.bst:components/zstd.bst
- filename: runtimes/nspawn-org-bin.bst
  config:
    location: /runtime

sources:
- kind: local
  path: files/runtimes/nspawn/sysext
  directory: sysext-src

variables:
  strip-binaries: ""
  sysext-name: "nspawn-org"
  sysext-image: "nspawn-org_%{image-version}"
  sysext-release: "nspawn-org_%{image-version}"
  sysext-version: "%{image-version}"
  sysext-architecture: "%{systemd-arch}"

config:
  install-commands:
  - |
    mkdir -p sysext%{bindir}
    install -m0755 /runtime%{bindir}/nspawn sysext%{bindir}/nspawn
  - "%{sysext-pack}"
