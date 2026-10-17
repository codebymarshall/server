kind: manual
description: |
  FSDK-native Podman with its complete runtime closure, never a nested engine in
  Homebrew. The junction pins Podman 6.1, conmon, crun, netavark, aardvark-dns,
  passt, fuse-overlayfs and containers-common. Private paths keep kubeadm's runc
  and containerd untouched. Only shared libraries and engine helpers are copied;
  no second systemd, shell, account database or host /usr/bin provider ships.

build-depends:
- base/base-stack.bst
- freedesktop-sdk.bst:components/findutils.bst
- freedesktop-sdk.bst:components/patchelf.bst
- filename: freedesktop-sdk.bst:components/podman.bst
  config:
    location: /podman
- filename: freedesktop-sdk.bst:components/aardvark-dns.bst
  config:
    location: /podman

sources:
- kind: local
  path: files/runtimes/podman
  directory: runtime-src

variables:
  strip-binaries: ""

config:
  install-commands:
  - |
    set -eu
    root="%{install-root}/usr/lib/bluefin/podman"
    install -d "$root/bin" "$root/lib" "$root/libexec/podman" "$root/share/containers"
    for b in podman crun pasta passt fuse-overlayfs catatonit nft; do
      install -m0755 "/podman%{bindir}/$b" "$root/bin/$b"
    done
    install -m0755 "/podman%{bindir}/conmon" "$root/libexec/podman/conmon"
    for b in netavark aardvark-dns rootlessport; do
      install -m0755 "/podman%{libexecdir}/podman/$b" "$root/libexec/podman/$b"
    done
    # Dereference soname links into this private library directory. Host ABI is
    # exactly the same pinned FSDK; no foreign loader or shared /usr libs merge.
    find "/podman%{libdir}" -maxdepth 1 \( -type f -o -type l \) -name '*.so*' \
      -exec cp -L -t "$root/lib" {} +
    # OCI runtimes must work without the wrapper's loader environment.
    # Give crun and its private library independent, relative loader paths.
    patchelf --set-rpath '$ORIGIN/../lib' "$root/bin/crun"
    for library in "$root"/lib/libcrun.so*; do
      patchelf --set-rpath '$ORIGIN' "$library"
    done
    cp -a "/podman%{datadir}/containers/." "$root/share/containers/"
    install -d "$root/etc/containers"
    cp -a /podman/etc/containers/. "$root/etc/containers/"
    install -m0644 runtime-src/containers.conf "$root/containers.conf"
    install -m0644 runtime-src/storage.conf "$root/storage.conf"
    install -m0644 runtime-src/tmpfiles.conf "$root/tmpfiles.conf"
    install -m0755 runtime-src/podman "$root/podman"
