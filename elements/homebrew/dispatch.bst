kind: manual
description: |
  Fixed-machine transparent Homebrew dispatch, immutable guest lifecycle,
  persistent prefix seeding, exact host account mapping, and lingering guest
  user scopes. The host ships no native Homebrew or package binaries.

build-depends:
- base/base-stack.bst
- freedesktop-sdk.bst:public-stacks/buildsystem-autotools.bst
- freedesktop-sdk.bst:components/systemd-libs.bst

depends:
- freedesktop-sdk.bst:public-stacks/runtime-gnu.bst
- freedesktop-sdk.bst:components/systemd.bst
- freedesktop-sdk.bst:components/systemd-libs.bst
- freedesktop-sdk.bst:components/python3.bst
- freedesktop-sdk.bst:components/util-linux-full.bst
- freedesktop-sdk.bst:components/shadow.bst
- freedesktop-sdk.bst:components/sudo.bst

sources:
- kind: local
  path: files/homebrew

config:
  build-commands:
  - |
    cc -std=c11 -O2 -Wall -Wextra -Werror $(pkg-config --cflags libsystemd) \
      dispatch.c -o bluefin-homebrew-enter $(pkg-config --libs libsystemd)
  install-commands:
  - |
    install -Dm0755 bluefin-homebrew-enter "%{install-root}/usr/libexec/bluefin-homebrew-enter"
    ln -s bluefin-homebrew-enter "%{install-root}/usr/libexec/bluefin-homebrew-command"
    install -d "%{install-root}/usr/bin"
    ln -s ../libexec/bluefin-homebrew-enter "%{install-root}/usr/bin/brew"
    install -Dm0755 bluefin-homebrew-prepare bluefin-homebrew-ready -t "%{install-root}/usr/libexec/"
    install -Dm0440 bluefin-homebrew.sudoers -t "%{install-root}/usr/lib/bluefin-homebrew/"
    install -Dm0644 90-bluefin-homebrew.sh sshd.conf -t "%{install-root}/usr/lib/bluefin-homebrew/"
    install -Dm0644 bluefin-homebrew.tmpfiles "%{install-root}/usr/lib/tmpfiles.d/50-bluefin-homebrew.conf"
    install -Dm0644 systemd/*.service systemd/*.path -t "%{install-root}/usr/lib/systemd/system/"
    install -Dm0644 80-bluefin-homebrew.preset "%{install-root}/usr/lib/systemd/system-preset/80-bluefin-homebrew.preset"
    install -Dm0644 guest/bluefin-homebrew-guest.service -t "%{install-root}/usr/lib/bluefin-homebrew/guest/"
    install -Dm0755 guest/bootstrap guest/command -t "%{install-root}/usr/lib/bluefin-homebrew/guest/"
    install -d "%{install-root}/usr/lib/systemd/system/bluefin-homebrew.service.wants"
    ln -s ../bluefin-homebrew-commands.path "%{install-root}/usr/lib/systemd/system/bluefin-homebrew.service.wants/bluefin-homebrew-commands.path"
