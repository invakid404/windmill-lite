# Releasing windmill-lite

A release of windmill-lite is the Git tag `v<version>`, where `<version>`
is the `version` field of
[harnesses/package.json](harnesses/package.json), such as `v0.1.0`. That
one version is the version of the npm package `windmill-lite` and of the
engine ([scripts/release.py](scripts/README.md#release-helper)).

When the tag is pushed, the
[release workflow](.github/workflows/release.yml) releases it:

1. It checks that the tag is `v` + the package's version.
2. It builds the engine for Linux amd64 and arm64 (static binaries) and
   for macOS arm64 and amd64. Each binary must print
   `windmill-lite v<version>` and pass `selfcheck`.
3. It packs the npm package and runs the
   [packaging test](harnesses/README.md#test-the-package) on that tarball
   with the Linux amd64 engine.
4. It creates the GitHub Release of the tag, with the four binaries and
   their SHA-256 sums in `SHA256SUMS`.
5. It publishes the tested tarball to npm with provenance, through npm
   trusted publishing. A version that is on npm already is not published
   again.

Trusted publishing needs no npm token or secret. npm exchanges the publish
job's GitHub OIDC token for a short-lived publish token, and accepts it
only from the workflow file `release.yml` of this repository. A
pre-release version, such as `0.2.0-rc.1`, becomes a GitHub pre-release
and the npm dist-tag `next`.

## Publish the first version by hand

npm adds a trusted publisher only to a package that exists, so the owner
publishes the first version, `0.1.0`, once, by hand. Every later version
comes from the release workflow.

1. Check out the commit to publish, on `main`.
2. Check that `python3 scripts/release.py version` prints `0.1.0`.
3. Get a Bun 1.3.10 executable, such as `bun-darwin-aarch64.zip` from
   [Bun's release bun-v1.3.10](https://github.com/oven-sh/bun/releases/tag/bun-v1.3.10).
   The package's build refuses any other Bun.
4. Point `WL_BUN` at it: `export WL_BUN=/path/to/bun-1.3.10/bun`.
5. Change to the package's root: `cd harnesses`.
6. Install the build's dependencies: `"$WL_BUN" install --frozen-lockfile`.
7. Sign in to npm as the account that will own the package: `npm login`.
8. Publish the package: `npm publish --access public`. npm runs the build
   (the `prepack` script) with `WL_BUN`, and publishes
   `windmill-lite@0.1.0`.
9. On [npmjs.com](https://www.npmjs.com/package/windmill-lite), open the
   package's **Settings**. Under **Trusted publishing**, choose
   **GitHub Actions** and enter these fields:

   | Field | Value |
   |---|---|
   | Organization or user | `invakid404` |
   | Repository | `windmill-lite` |
   | Workflow filename | `release.yml` |
   | Environment name | Leave it empty |

10. Save the trusted publisher.
11. Optional, and npm's recommendation once a trusted publisher exists:
    under **Publishing access**, choose **Require two-factor
    authentication and disallow tokens**. Then only the release workflow
    and a two-factor sign-in can publish.
12. Tag the commit that you published from: `git tag v0.1.0 <commit>`.
13. Push the tag: `git push origin v0.1.0`.

The release workflow then creates the GitHub Release `v0.1.0` with the
engine binaries. It finds `0.1.0` on npm already, so it publishes nothing.
Its notice says whether the published tarball equals the one that it built.

## Release a new version

1. In a pull request, set `version` in `harnesses/package.json` to the new
   version, such as `0.2.0`.
2. Merge the pull request once CI passes.
3. Tag the merge commit on `main`: `git tag v0.2.0 <merge commit>`.
4. Push the tag: `git push origin v0.2.0`.
5. Watch the run of the release workflow under **Actions**. When it
   passes, the GitHub Release and the npm package `windmill-lite@0.2.0`
   exist.

A failed run creates nothing after the step that failed. When the publish
fails because of npm's settings, such as a missing trusted publisher, fix
them and re-run the failed jobs. A cause in the code needs a new version
and a new tag, because a release runs only from its tag.

## Try a release without releasing

A dry run builds and checks everything that a release does, runs
`npm publish --dry-run`, and creates and publishes nothing.

- Run the release workflow by hand on `main`: **Actions**, **Release**,
  **Run workflow**, with **Dry run** checked (the default). Or run
  `gh workflow run release.yml -f dry_run=true`.
- A pull request that changes the release workflow, the package's
  manifest, lockfile, build or test, or `scripts/release.py` runs the
  workflow as a dry run.

A dry run on a branch has no tag to check. The workflow refuses to release,
with **Dry run** unchecked, from anything but a tag.
