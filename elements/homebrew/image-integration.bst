kind: manual
description: Select the version-locked Homebrew machine before runtime preparation.

build-depends:
- base/base-stack.bst

sources:
- kind: local
  path: files/homebrew-image

variables:
  strip-binaries: ""

config:
  install-commands:
  - install -Dm0755 bluefin-homebrew-image "%{install-root}/usr/libexec/bluefin-homebrew-image"
  - install -Dm0644 bluefin-homebrew-image.service "%{install-root}/usr/lib/systemd/system/bluefin-homebrew-image.service"
  - install -Dm0644 10-image.conf "%{install-root}/usr/lib/systemd/system/bluefin-homebrew-prepare.service.d/10-image.conf"
