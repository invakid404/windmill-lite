// Command windmill-lite is the windmill-lite command-line entry point.
//
// It reports its version, builds a bundle from a Windmill sync folder,
// verifies a bundle against its manifest or a release manifest, checks that
// its expression engine starts, and serves host mode for the Bun host SDK
// with the walking skeleton (`windmill-lite host`, milestone W1). It runs
// a bundle's flow or script with package wl and prints the outcome as JSON
// (`windmill-lite run`, ADR 0009).
package main

import (
	"flag"
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

const usage = `usage: windmill-lite [version]
       windmill-lite build <folder> -o <bundle> [flags]
       windmill-lite verify <bundle> [-manifest <release manifest>] [-digest <digest>]
       windmill-lite run <path> --bundle <bundle> [--args <json|@file>] [flags]
       windmill-lite selfcheck
       windmill-lite host --bundle <dir> [--kill-after <duration>]`

// run executes the command with args (without the program name) and returns
// the process exit code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (len(args) == 1 && isVersionArg(args[0])) {
		fmt.Fprintf(stdout, "windmill-lite %s\n", resolveVersion())
		return 0
	}
	switch args[0] {
	case "build":
		return runBuild(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "run":
		return runRun(args[1:], stdout, stderr)
	case "selfcheck":
		return runSelfcheck(args[1:], stdout, stderr)
	case "host":
		return runHost(args[1:], stderr)
	}
	fmt.Fprintln(stderr, usage)
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

// parse parses a command's flags, which may come before or after its
// positional arguments, and returns the positional arguments. It returns
// false, having printed why, when the arguments do not parse.
func parse(fs *flag.FlagSet, args []string) ([]string, bool) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, false
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, true
		}
		pos, args = append(pos, args[0]), args[1:]
	}
}

func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}
