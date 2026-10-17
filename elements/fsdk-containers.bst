kind: junction

sources:
- kind: git_repo
  url: https://github.com/projectbluefin/fsdk-containers.git
  track: feat/server-homebrew-20261011
  ref: ae5bf888b72693bb6774cd56d21da4677efc2e36

config:
  options:
    arch: '%{arch}'
  overrides:
    plugins/buildstream-plugins-community.bst: plugins/buildstream-plugins-community.bst
