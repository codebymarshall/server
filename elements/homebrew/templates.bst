kind: manual
description: |
  Home Server installer profiles. The optional cluster variant composes the
  canonical Home and existing Homelab control-plane templates offline.

build-depends:
- base/base-stack.bst
- freedesktop-sdk.bst:components/python3.bst
- freedesktop-sdk.bst:components/python3-pyyaml.bst

sources:
- kind: local
  path: files/homebrew/templates
  directory: home
- kind: local
  path: files/homelab/templates/homelab-control-plane.bu
  directory: cluster
- kind: local
  path: files/homebrew-image/compose-profile.py

variables:
  strip-binaries: ""

config:
  install-commands:
  - install -Dm0644 home/home-server.bu "%{install-root}/home-server.bu"
  - python3 compose-profile.py home/home-server.bu cluster/homelab-control-plane.bu "%{install-root}/home-server-cluster.bu"
