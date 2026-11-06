#!/bin/sh
# The packaging test of the npm package windmill-lite (harnesses/package.json).
# It checks the package as a host gets it from npm, and exits 1 at the
# first check that fails:
#
# 1. npm pack packs the package, which builds it first (its prepack
#    script). With WL_TARBALL set, it checks that tarball instead.
# 2. The tarball holds exactly the files of files.txt, so no test,
#    fixture, build tooling or Go code ships. Its manifest has the fields
#    that npm and provenance need, and its LICENSE is the repository's.
# 3. bun add installs the tarball into a copy of project/, an empty
#    project with the tsconfig.json of bun init (Bun 1.3.10) and
#    TypeScript.
# 4. tsc type-checks project/smoke.ts, which imports windmill-lite/bun and
#    windmill-lite/bun/testing, and project/testing.ts, which imports
#    windmill-lite/bun/testing: TypeScript 5.9.3 with bun init's
#    tsconfig.json, then TypeScript 5.9.3 and 6.0.3 with
#    tsconfig.nodenext.json (NodeNext resolution, with the declarations of
#    the package and of Bun checked too). TypeScript 5.9.3 and 6.0.3 also
#    check testing.ts with tsconfig.node.json: Node's type declarations
#    only, so the test entry's declarations need no Bun types.
# 5. project/testing.ts checks the SDK's test entry, windmill-lite/bun/testing,
#    on Bun 1.4.3, and on the node on the PATH, which must be Node 22 or
#    newer, as the JavaScript that tsc emits from it (tsconfig.node.json).
# 6. The engine of this commit, stamped with the package's version as a
#    release stamps it (scripts/release.py build-engine), builds a bundle
#    of folder/, and the smoke host runs the flow f/smoke/hello on it
#    through the installed SDK. The smoke host also loads the SDK's test
#    entry, and checks that the two share one runtime state. With
#    WL_ENGINE set, it uses that engine instead.
# 7. An engine stamped with another version, and a bundle that engine
#    built: the smoke host's connect() must reject with version_mismatch.
#    So the packed SDK carries the package's version; an unstamped SDK
#    would pair with any engine.
#
# Needs Bun 1.4.3 (WL_BUN, default bun), Node 22 or newer as node on the
# PATH, npm, python3, go with a C compiler, and the dependencies of the
# package (bun install --frozen-lockfile in harnesses/) and of the build
# helper (the same in harnesses/bun/build).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
pkg=$(cd "$here/../.." && pwd)
repo=$(cd "$pkg/.." && pwd)
WL_BUN=${WL_BUN:-bun}
export WL_BUN
WL_BUILD_HELPER="$repo/harnesses/bun/build/main.ts"
export WL_BUILD_HELPER

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fail() {
  echo "check.sh: $*" >&2
  exit 1
}
step() {
  echo "== $*"
}
# absolute prints its argument as an absolute path, since the checks run
# in other directories.
absolute() {
  case $1 in
    /*) printf '%s\n' "$1" ;;
    *) printf '%s\n' "$PWD/$1" ;;
  esac
}

version=$(python3 "$repo/scripts/release.py" version)

if [ -n "${WL_TARBALL:-}" ]; then
  tgz=$(absolute "$WL_TARBALL")
else
  step "npm pack windmill-lite $version"
  (cd "$pkg" && npm pack --pack-destination "$work") || fail "npm pack failed"
  tgz="$work/windmill-lite-$version.tgz"
fi
[ -f "$tgz" ] || fail "no tarball $tgz"

step "the packed files are files.txt"
tar -tzf "$tgz" | LC_ALL=C sort > "$work/files.txt"
diff -u "$here/files.txt" "$work/files.txt" || fail "the packed files differ from files.txt (- want, + got)"
mkdir "$work/unpacked"
tar -xzf "$tgz" -C "$work/unpacked"
cmp "$repo/LICENSE" "$work/unpacked/package/LICENSE" || fail "the packed LICENSE is not the repository's"
python3 - "$work/unpacked/package/package.json" "$version" <<'EOF' || fail "the packed package.json"
import json
import sys

manifest = json.load(open(sys.argv[1], encoding="utf-8"))
want = {
    "name": "windmill-lite",
    "version": sys.argv[2],
    "license": "Unlicense",
    "type": "module",
    "exports": {
        "./bun": {"types": "./dist/bun/host/index.d.ts", "default": "./dist/bun/host/index.js"},
        "./bun/testing": {"types": "./dist/bun/host/testing.d.ts", "default": "./dist/bun/host/testing.js"},
    },
    "publishConfig": {"access": "public"},
}
problems = [f"{k} is {manifest.get(k)!r}, want {v!r}" for k, v in want.items() if manifest.get(k) != v]
# npm provenance checks the repository against the repository that runs
# the workflow.
url = (manifest.get("repository") or {}).get("url")
if url != "git+https://github.com/invakid404/windmill-lite.git":
    problems.append(f"repository.url is {url!r}")
# The bundled SDK imports only Bun's built-in modules.
for k in ("dependencies", "peerDependencies", "optionalDependencies", "bundleDependencies"):
    if k in manifest:
        problems.append(f"it has {k}")
for p in problems:
    print(f"package.json: {p}", file=sys.stderr)
sys.exit(1 if problems else 0)
EOF

step "bun add the tarball into an empty project"
cp -R "$here/project" "$work/project"
(cd "$work/project" && "$WL_BUN" install --frozen-lockfile && "$WL_BUN" add "$tgz") || fail "bun add failed"

step "tsc: TypeScript 5.9.3, bun init's tsconfig.json"
(cd "$work/project" && "$WL_BUN" node_modules/typescript/bin/tsc -p tsconfig.json --pretty false) || fail "tsc"
step "tsc: TypeScript 5.9.3, NodeNext"
(cd "$work/project" && "$WL_BUN" node_modules/typescript/bin/tsc -p tsconfig.nodenext.json --pretty false) || fail "tsc"
step "tsc: TypeScript 6.0.3, NodeNext"
(cd "$work/project" && "$WL_BUN" node_modules/typescript-6/bin/tsc -p tsconfig.nodenext.json --pretty false) || fail "tsc"
step "tsc: TypeScript 5.9.3 and 6.0.3, NodeNext with Node's types only"
(cd "$work/project" && "$WL_BUN" node_modules/typescript/bin/tsc -p tsconfig.node.json --noEmit --pretty false) || fail "tsc"
(cd "$work/project" && "$WL_BUN" node_modules/typescript-6/bin/tsc -p tsconfig.node.json --noEmit --pretty false) || fail "tsc"

step "the test entry on Bun $("$WL_BUN" --version)"
(cd "$work/project" && "$WL_BUN" testing.ts) || fail "the test entry on Bun"
node_version=$(node -p 'process.versions.node') || fail "no node on the PATH"
[ "${node_version%%.*}" -ge 22 ] || fail "the test entry needs Node 22 or newer, and node on the PATH is $node_version"
step "the test entry on Node $node_version"
(cd "$work/project" && "$WL_BUN" node_modules/typescript/bin/tsc -p tsconfig.node.json --pretty false) || fail "tsc of testing.ts for Node"
(cd "$work/project" && node node/testing.js) || fail "the test entry on Node"

if [ -n "${WL_ENGINE:-}" ]; then
  engine=$(absolute "$WL_ENGINE")
else
  step "build the engine stamped as a release stamps it"
  engine="$work/windmill-lite"
  python3 "$repo/scripts/release.py" build-engine "$engine" || fail "build-engine"
fi
python3 "$repo/scripts/release.py" check-engine "$engine" || fail "the engine is not version $version"

step "the smoke host runs f/smoke/hello on engine $version"
"$engine" build "$here/folder" -o "$work/bundle" || fail "windmill-lite build"
mkdir "$work/host"
(cd "$work/host" && WL_ENGINE="$engine" WL_BUNDLE="$work/bundle" "$WL_BUN" "$work/project/smoke.ts" run) || fail "the smoke host"

step "an engine of another version refuses the smoke host"
other="$work/windmill-lite-other"
(cd "$repo" && go build -o "$other" -ldflags "-X main.version=v0.0.0-other" ./cmd/windmill-lite) || fail "go build"
"$other" build "$here/folder" -o "$work/bundle-other" || fail "windmill-lite build"
mkdir "$work/host-other"
(cd "$work/host-other" && WL_ENGINE="$other" WL_BUNDLE="$work/bundle-other" "$WL_BUN" "$work/project/smoke.ts" mismatch) || fail "the smoke host against another version"

echo "check.sh: windmill-lite $version packs, installs, type-checks and runs"
