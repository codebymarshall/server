kind: manual
description: |
  Wrap the reusable FSDK Homebrew machine in the Server image-version axis.
  The immutable machine updates with the host; the owner's writable prefix
  is seeded once and is never replaced by an OS update.

(@):
- include/image.yml

build-depends:
- base/base-stack.bst
- filename: fsdk-containers.bst:oci/brew-nspawn.bst
  config:
    location: /producer

variables:
  strip-binaries: ""

config:
  install-commands:
  - |
    set -euo pipefail
    cp /producer/homebrew-env-*.raw.zst "%{install-root}/homebrew_%{image-version}.raw.zst"
    (cd "%{install-root}" && sha256sum --binary "homebrew_%{image-version}.raw.zst" > SHA256SUMS)
