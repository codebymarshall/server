#!/bin/sh
# Usage: prune.sh ROOT LIST...
#
# Deletes every path matching a glob in the LIST files (relative to ROOT).
# Blank lines and '#' comments are ignored. A pattern that matches nothing is
# an error, so the lists cannot silently go stale after an FSDK bump.
set -eu

root="$1"
shift

cat "$@" | while IFS= read -r line; do
  pattern="$(printf '%s' "${line%%#*}" | tr -d '[:space:]')"
  [ -n "${pattern}" ] || continue
  # shellcheck disable=SC2086 # the pattern is a glob; it must expand
  set -- "${root}"/${pattern}
  if [ ! -e "$1" ] && [ ! -L "$1" ]; then
    echo "prune: pattern matches nothing: ${pattern}" >&2
    exit 1
  fi
  for path; do
    echo "prune: ${path#"${root}"/}"
  done
  rm -rf -- "$@"
done
