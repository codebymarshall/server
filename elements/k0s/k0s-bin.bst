kind: manual
description: |
  Import the upstream k0s release binary for the build architecture.

  The release tag comes from include/k0s.yml (%{k0s-upstream-tag}); only the
  SHA256 of each architecture's binary is restated here, taken from that
  release's sha256sums.txt asset.

# %{k0s-upstream-tag} — the upstream release tag, URL-escaped. Single source
# of truth for the k0s version axis; see include/k0s.yml.
(@):
  - include/arch.yml
  - include/k0s.yml

build-depends:
  # Base runtime providing /bin/sh and install in the sandbox.
  - base/base-stack.bst

variables:
  # Do not try to strip the pre-built upstream binary.
  strip-binaries: ""

sources:
  - kind: remote
    url: github:k0sproject/k0s/releases/download/%{k0s-upstream-tag}/k0s-%{k0s-upstream-tag}-amd64
    ref: 377cdb372f37a3c23f1f6072e4f11838b45b463b458f2afdd0c2607157fbe04f

(?):
  - arch == "aarch64":
      sources:
        - kind: remote
          url: github:k0sproject/k0s/releases/download/%{k0s-upstream-tag}/k0s-%{k0s-upstream-tag}-arm64
          ref: 88d730866e610fe6ad32e877bf79ed10eafd48fe14e699fff6f5133c11d598b7

config:
  install-commands:
    - install -D -m 0755 k0s-%{k0s-upstream-tag}-%{go-arch} "%{install-root}/usr/bin/k0s"
