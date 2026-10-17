kind: compose
description: |
  Runtime-only closure for the optional Homebrew integration, including its
  Python interpreter and extension modules. It is never part of the base OS.

build-depends:
- homebrew/dispatch.bst
- homebrew/image-integration.bst
- homebrew/owner-integration.bst

config:
  exclude:
  - debug
  - devel
  - doc
  - locale
  - tests
  include-orphans: True
  integrate: False
