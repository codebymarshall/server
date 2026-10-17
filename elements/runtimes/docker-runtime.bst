kind: manual
description: |
  Static Docker plus FSDK-built nft-compatible iptables and its library/plugin
  closure under a private prefix. Never installs a shared containerd, runc,
  iptables, system unit or root socket from another runtime provider.

build-depends:
- base/base-stack.bst
- freedesktop-sdk.bst:components/findutils.bst
- filename: runtimes/docker-bin.bst
  config:
    location: /docker
- filename: freedesktop-sdk.bst:components/iptables.bst
  config:
    location: /firewall

variables:
  strip-binaries: ""

config:
  install-commands:
  - |
    set -eu
    root="%{install-root}/usr/lib/bluefin/docker"
    mkdir -p "$root"
    cp -a /docker/usr/lib/bluefin/docker/. "$root/"
    install -d "$root/lib" "$root/lib/xtables"
    install -m0755 /firewall%{bindir}/xtables-nft-multi "$root/bin/xtables-nft-multi"
    for b in iptables iptables-save iptables-restore ip6tables ip6tables-save ip6tables-restore; do
      ln -s xtables-nft-multi "$root/bin/$b"
    done
    find /firewall%{libdir} -maxdepth 1 \( -type f -o -type l \) -name '*.so*' \
      -exec cp -L -t "$root/lib" {} +
    cp -a /firewall%{libdir}/xtables/. "$root/lib/xtables/"
