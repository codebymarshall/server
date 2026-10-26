kind: manual
description: |
  Opt-in systemd-sysext with the Tailscale VPN daemon and CLI tool.

(@):
- include/arch.yml
- include/image.yml
- include/tailscale.yml
- include/sysext.yml

build-depends:
- base/base-stack.bst
- freedesktop-sdk.bst:components/erofs-utils.bst
- freedesktop-sdk.bst:components/zstd.bst
- filename: runtimes/tailscale-bin.bst
  config:
    location: /runtime

sources:
- kind: local
  path: files/tailscale/sysext
  directory: sysext-src

variables:
  strip-binaries: ""
  sysext-name: "tailscale"
  sysext-image: "tailscale_%{image-version}"
  sysext-release: "tailscale_%{image-version}"
  sysext-version: "%{image-version}"
  sysext-architecture: "%{systemd-arch}"

config:
  install-commands:
  - |
    set -euo pipefail
    mkdir -p sysext%{bindir} \
             sysext%{indep-libdir}/systemd/system \
             sysext%{indep-libdir}/systemd/system-preset \
             sysext%{datadir}/bluefin/tailscale

    install -m0755 /runtime%{bindir}/tailscale sysext%{bindir}/tailscale
    install -m0755 /runtime%{bindir}/tailscaled sysext%{bindir}/tailscaled
    install -m0644 /runtime%{indep-libdir}/systemd/system/tailscaled.service sysext%{indep-libdir}/systemd/system/tailscaled.service
    install -m0644 /runtime%{indep-libdir}/systemd/system/tailscale-wait-online.service sysext%{indep-libdir}/systemd/system/tailscale-wait-online.service
    install -m0644 /runtime%{indep-libdir}/systemd/system/tailscale-online.target sysext%{indep-libdir}/systemd/system/tailscale-online.target
    install -m0644 /runtime%{datadir}/bluefin/tailscale/tailscaled.defaults sysext%{datadir}/bluefin/tailscale/tailscaled.defaults
    install -m0644 sysext-src/80-tailscale.preset sysext%{indep-libdir}/systemd/system-preset/80-tailscale.preset
  - "%{sysext-pack}"
