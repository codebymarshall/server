kind: manual
description: |
  Optional real host docker system extension, image-locked and delivered through
  the signed Bluefin release. Rootless owner units are opt-in; rootful API sockets
  are never created or exposed by this integration.
  Private /usr/lib/bluefin binaries and independent state coexist with kubeadm.

(@):
- include/arch.yml
- include/image.yml
- include/runtimes.yml
- include/sysext.yml

build-depends:
- base/base-stack.bst
- freedesktop-sdk.bst:components/erofs-utils.bst
- freedesktop-sdk.bst:components/zstd.bst
- filename: runtimes/docker-runtime.bst
  config:
    location: /runtime

sources:
- kind: local
  path: files/runtimes/docker
  directory: sysext-src

variables:
  strip-binaries: ""
  sysext-name: "docker"
  sysext-image: "docker_%{image-version}"
  sysext-release: "docker_%{image-version}"
  sysext-version: "%{image-version}"
  sysext-architecture: "%{systemd-arch}"

config:
  install-commands:
  - |
    set -eu
    mkdir -p sysext/usr/lib/bluefin
    cp -a /runtime/usr/lib/bluefin/docker sysext/usr/lib/bluefin/
    lib="sysext%{indep-libdir}"
    install -d "$lib/systemd/user"
    install -m0644 sysext-src/bluefin-docker.service "$lib/systemd/user/"
    install -D -m0644 sysext-src/80-bluefin-docker.preset "$lib/systemd/user-preset/80-bluefin-docker.preset"
  - "%{sysext-pack}"
