// Command windmill-lite is the windmill-lite command-line entry point.
//
// It reports its version, and serves host mode for the Bun host SDK with
// the walking skeleton (`windmill-lite host`, milestone W1). The commands
// that build a Windmill folder and run its flows do not exist yet.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

// version is set at link time with -ldflags "-X main.version=<version>".
// When it is empty, the module version from the build info is used.
var version string

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run executes the command with args (without the program name) and returns
// the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (len(args) == 1 && isVersionArg(args[0])) {
		fmt.Fprintf(stdout, "windmill-lite %s\n", resolveVersion())
		return 0
	}
	if args[0] == "host" {
		return runHost(args[1:], stderr)
	}
	fmt.Fprintln(stderr, "usage: windmill-lite [version] | windmill-lite host --bundle <dir> [--kill-after <duration>]")
	return 2
}

func isVersionArg(arg string) bool {
	switch arg {
	case "version", "-version", "--version", "-v":
		return true
	}
	return false
}

func resolveVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" {
		return info.Main.Version
	}
	return "(devel)"
}
