kind: manual
description: |
  Opt-in Homebrew host integration, locked to the booted OS version. Contains
  the real command bridge, lifecycle units, owner orchestration, profile and
  sudoers hooks, plus the complete Python runtime absent from the base OS.
  The immutable Homebrew machine is a separate asset in /var/lib/machines;
  the homebrew sysupdate feature delivers both assets in lock-step.

(@):
- include/arch.yml
- include/image.yml
- include/sysext.yml

build-depends:
- base/base-stack.bst
- freedesktop-sdk.bst:components/erofs-utils.bst
- freedesktop-sdk.bst:components/findutils.bst
- freedesktop-sdk.bst:components/zstd.bst
- filename: homebrew/runtime.bst
  config:
    location: /integration
- filename: bluefin-server/os-filesystem.bst
  config:
    location: /base

sources:
- kind: local
  path: files/homebrew/sysext
  directory: sysext-src

variables:
  strip-binaries: ""
  sysext-name: "homebrew-integration"
  sysext-image: "homebrew-integration_%{image-version}"
  sysext-release: "homebrew-integration_%{image-version}"
  sysext-version: "%{image-version}"
  sysext-architecture: "%{systemd-arch}"

config:
  install-commands:
  - |
    set -euo pipefail
    mkdir -p sysext
    cp -a /integration%{prefix} sysext%{prefix}
    # Both closures use the same pinned FSDK. Keep only files the base lacks,
    # rather than shadowing its libraries, identity, presets or other services.
    # This retains Python's entire runtime closure, including extension modules
    # and their transitive libraries, without adding Python to the base image.
    while IFS= read -r -d '' path; do
      base="/base/${path#sysext/}"
      if [ -e "$base" ] || [ -L "$base" ]; then
        rm -f "$path"
      fi
    done < <(find sysext%{prefix} \( -type f -o -type l \) -print0)
    find sysext%{prefix} -depth -type d -empty -delete
  - "%{sysext-pack}"
