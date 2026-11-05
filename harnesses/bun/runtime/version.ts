// The release this harness and host SDK belong to (ADR 0009: a host takes
// the SDK of its engine's release, and the SDK's version is the engine's).
// The hello names it as the harness version. A release stamps it by
// running or building with `--define WL_RELEASE='"vX.Y.Z"'`: the npm
// package's build (harnesses/npm/build.ts) stamps "v" + the package's
// version, which is also the release engine's version
// (scripts/release.py). The repository's own is "dev", which pairs with
// any engine.

declare const WL_RELEASE: string | undefined;

export const HARNESS_VERSION: string = typeof WL_RELEASE === "string" ? WL_RELEASE : "dev";

/** Whether a host SDK of version sdk pairs with an engine of version
 * engine (conformance/protocol/host/versions.json, the same rule as the
 * engine's VersionsPair): an unstamped version ("", "dev", "(devel)")
 * pairs with anything; two stamped ones pair when equal, a leading "v"
 * aside. */
export function versionsPair(sdk: string, engine: string): boolean {
  const unstamped = (v: string) => v === "" || v === "dev" || v === "(devel)";
  if (unstamped(sdk) || unstamped(engine)) return true;
  return sdk.replace(/^v/, "") === engine.replace(/^v/, "");
}
