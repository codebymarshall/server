kind: junction

sources:
- kind: git_repo
  url: https://github.com/projectbluefin/fsdk-containers.git
  track: main
  ref: d75f216e03f0214e7a2eb41ee2f66df5f46fd5da

config:
  options:
    arch: '%{arch}'
  overrides:
    plugins/buildstream-plugins-community.bst: plugins/buildstream-plugins-community.bst
