#!/bin/sh
# Typechecks the Bun host SDK (harnesses/bun/host) and the harness runtime
# (harnesses/bun/runtime) as a host that vendors them as source sees them
# (ADR 0009): copied into a package of type module, against the type
# declarations of the pinned Bun (@types/bun 1.3.10, with TypeScript, from
# this directory's lockfile). It runs tsc four times: with and without the
# DOM library, each in strict and in non-strict mode, and exits 1 if any
# run reports an error.
#
# Run `bun install --frozen-lockfile` in this directory first.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
src=$(cd "$here/../.." && pwd)
tsc="$here/node_modules/.bin/tsc"
if [ ! -x "$tsc" ]; then
  echo "check.sh: no tsc; run bun install --frozen-lockfile in $here" >&2
  exit 2
fi
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cp -R "$src/host" "$src/runtime" "$here/tsconfig.json" "$work/"
printf '{"type":"module"}\n' > "$work/package.json"
ln -s "$here/node_modules" "$work/node_modules"
status=0
for lib in ESNext ESNext,DOM; do
  for strict in true false; do
    echo "tsc --lib $lib --strict $strict"
    (cd "$work" && "$tsc" -p tsconfig.json --pretty false --lib "$lib" --strict "$strict") || status=1
  done
done
exit "$status"
