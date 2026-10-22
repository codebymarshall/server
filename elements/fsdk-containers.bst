kind: junction

sources:
- kind: git_repo
  url: https://github.com/projectbluefin/fsdk-containers.git
  track: main
  ref: 5b2df6e22b2dd69ec4e86d9283eaac6def355915

config:
  options:
    arch: '%{arch}'
  overrides:
    plugins/buildstream-plugins-community.bst: plugins/buildstream-plugins-community.bst
