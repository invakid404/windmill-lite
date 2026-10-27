# cmd/windmill-lite

**Current state:** placeholder. This README covers the `windmill-lite`
command in this directory. The command prints its version and nothing else;
the commands that load a Windmill folder and run its flows do not exist yet.

## Usage

```sh
go run ./cmd/windmill-lite            # windmill-lite (devel)
go run ./cmd/windmill-lite version    # same; --version, -version and -v also work
```

Any other argument prints `usage: windmill-lite [version]` to standard error
and exits with status 2.

## Version

The printed version is, in order of preference:

1. the value linked in with `-ldflags "-X main.version=<version>"`;
2. the module version from the binary's build information, for example when
   it is built with `go install github.com/invakid404/windmill-lite/cmd/windmill-lite@<version>`;
3. `(devel)`.

`main_test.go` covers the version output, the link-time override and the
usage error.
