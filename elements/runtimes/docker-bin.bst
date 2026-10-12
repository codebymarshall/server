kind: manual
description: |
  Maintained upstream static Docker engine and real CLI, private containerd/runc,
  RootlessKit, slirp4netns, Compose and Buildx. All release assets are sha256-pinned
  for each architecture. The inspected bakery docker image uses /usr/bin/containerd
  and global rootful units, so cannot merge beside kubeadm; reuse its upstream
  artifact chain with a private /usr/lib/bluefin tree. Nothing is installed in the base.

(@):
- include/arch.yml
- include/runtimes.yml

build-depends:
- base/base-stack.bst

variables:
  strip-binaries: ""

sources:
- kind: tar
  url: https://download.docker.com/linux/static/stable/x86_64/docker-%{docker-version}.tgz
  ref: 33e1ab8b63d14bca449f7a3d30d7f6aa669daa544e60a86b17fec87f61afe2b4
  base-dir: docker
  directory: docker
- kind: tar
  url: https://download.docker.com/linux/static/stable/x86_64/docker-rootless-extras-%{docker-version}.tgz
  ref: 6b4a2e085a1e4ade5ba3de99fd770a0a9b53c719e6bf6b1632242bf4b068adb6
  base-dir: docker-rootless-extras
  directory: docker-rootless-extras
- kind: remote
  url: github:rootless-containers/slirp4netns/releases/download/v%{slirp4netns-version}/slirp4netns-x86_64
  ref: 411167abceff1fc8a7c562f53ed9f26d3ec8a17df4e2fe6eaa3bd53f1c1e1d48
  directory: slirp4netns
  filename: slirp4netns
- kind: remote
  url: github:docker/compose/releases/download/v%{docker-compose-version}/docker-compose-linux-x86_64
  ref: 40343e21ca777173e69cff5dbafeb37c6f81f3b0d57d9e597f036e95eb63e76a
  directory: plugins
  filename: docker-compose
- kind: remote
  url: github:docker/buildx/releases/download/v%{docker-buildx-version}/buildx-v%{docker-buildx-version}.linux-amd64
  ref: 4fe4cc38adf48169132749b6ca22a990928db0118e3407584ee553723115d287
  directory: plugins
  filename: docker-buildx

(?):
- arch == "aarch64":
    sources:
    - kind: tar
      url: https://download.docker.com/linux/static/stable/aarch64/docker-%{docker-version}.tgz
      ref: 811f6eee271678fc98dbe5c4d896c00fc5fe2daa4f7a37069c17abc036eab85b
      base-dir: docker
      directory: docker
    - kind: tar
      url: https://download.docker.com/linux/static/stable/aarch64/docker-rootless-extras-%{docker-version}.tgz
      ref: f1b76a0721101e15c1eabeccf08751c0d42206defe06422de2e4b8897fc0f129
      base-dir: docker-rootless-extras
      directory: docker-rootless-extras
    - kind: remote
      url: github:rootless-containers/slirp4netns/releases/download/v%{slirp4netns-version}/slirp4netns-aarch64
      ref: 045f85dd0bc51a61b99cdfe6bd22282cbc308b73caa64fc8859c11d3775c5817
      directory: slirp4netns
      filename: slirp4netns
    - kind: remote
      url: github:docker/compose/releases/download/v%{docker-compose-version}/docker-compose-linux-aarch64
      ref: 733ec76717ceb59052a9609b9dadfb523b2df8eab57a54212872d10a58078ea2
      directory: plugins
      filename: docker-compose
    - kind: remote
      url: github:docker/buildx/releases/download/v%{docker-buildx-version}/buildx-v%{docker-buildx-version}.linux-arm64
      ref: 38e890e1a162bfdbf32fbe91404991dc4fc28e55b0de901b34374a061bab4d2c
      directory: plugins
      filename: docker-buildx

config:
  install-commands:
  - |
    set -eu
    root="%{install-root}/usr/lib/bluefin/docker"
    install -d "$root/bin" "$root/lib/docker/cli-plugins"
    for b in docker dockerd docker-init docker-proxy containerd containerd-shim-runc-v2 ctr runc; do
      install -m0755 "docker/$b" "$root/bin/$b"
    done
    for b in rootlesskit dockerd-rootless.sh; do
      install -m0755 "docker-rootless-extras/$b" "$root/bin/$b"
    done
    install -m0755 slirp4netns/slirp4netns "$root/bin/slirp4netns"
    install -m0755 plugins/docker-compose plugins/docker-buildx "$root/lib/docker/cli-plugins/"
