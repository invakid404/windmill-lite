#!/bin/sh
# Typechecks the Bun host SDK (harnesses/bun/host) and the harness runtime
# (harnesses/bun/runtime) as a host that vendors them as source sees them
# (ADR 0009): copied into a package of type module, against the type
# declarations of the pinned Bun (@types/bun 1.3.10, with TypeScript).
#
# @types/bun accepts any @types/node, and a host brings its own, so the
# check runs against two sets of declarations, each pinned by its own
# lockfile:
#   - this directory: the newest @types/node;
#   - oldest/: the oldest @types/node a host is expected to use.
# For each set it runs tsc four times: with and without the DOM library,
# each in strict and in non-strict mode. It exits 1 if any run reports an
# error.
#
# Run `bun install --frozen-lockfile` in this directory and in oldest/
# first.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
src=$(cd "$here/../.." && pwd)
for types in "$here" "$here/oldest"; do
  if [ ! -x "$types/node_modules/.bin/tsc" ]; then
    echo "check.sh: no tsc; run bun install --frozen-lockfile in $types" >&2
    exit 2
  fi
done
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R "$src/host" "$src/runtime" "$here/tsconfig.json" "$work/"
printf '{"type":"module"}\n' > "$work/package.json"
status=0
for types in "$here" "$here/oldest"; do
  ln -sfn "$types/node_modules" "$work/node_modules"
  node=$(sed -n 's/^ *"version": *"\([^"]*\)".*/\1/p' "$types/node_modules/@types/node/package.json" | head -n 1)
  for lib in ESNext ESNext,DOM; do
    for strict in true false; do
      echo "@types/node $node: tsc --lib $lib --strict $strict"
      (cd "$work" && "$types/node_modules/.bin/tsc" -p tsconfig.json --pretty false --lib "$lib" --strict "$strict") || status=1
    done
  done
done
exit "$status"
