package main

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"time"

	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
)

// selfcheckProbes are programs whose results do not depend on the
// platform's C library: they exercise the engine's parser, bytecode,
// regular expressions, JSON, host strings and a recursion deep enough to
// need the slot threads' own stacks: about 1 KiB of C stack per call, so
// 800 calls fit under the engine's 1 MiB limit (JE-48 brackets the
// overflow above depth 1 000) but not in a thread with the C library's
// default stack, 128 KiB on musl (ADR 0003).
var selfcheckProbes = []struct {
	name, source, want string
	input              []expr.HostString
}{
	{
		name:   "language",
		source: `JSON.stringify([1 + 2, "ab".repeat(2), /a(b+)/.exec("abb")[1], Math.max(3, 7), 2 ** 10, [3, 1, 2].sort()])`,
		want:   `[3,"abab","bb",7,1024,[1,2,3]]`,
	},
	{
		name:   "host string",
		source: `(async () => JSON.stringify(JSON.parse(wl_input).items.map((x) => x * 2)))()`,
		input:  []expr.HostString{{Name: "wl_input", Text: []byte(`{"items":[1,2,3]}`)}},
		want:   `[2,4,6]`,
	},
	{
		name:   "deep recursion",
		source: `String((function f(n) { return n === 0 ? 0 : 1 + f(n - 1); })(800))`,
		want:   `800`,
	},
}

// runSelfcheck is `windmill-lite selfcheck`: it starts the expression
// engine, the QuickJS-ng pool, and runs selfcheckProbes on it. It is the
// check that the static Linux binary runs on an empty base image (ADR
// 0010, drift guard 5).
func runSelfcheck(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: windmill-lite selfcheck")
		return 2
	}
	start := time.Now()
	e, err := quickjs.New(quickjs.Options{Slots: 2})
	if err != nil {
		fmt.Fprintf(stderr, "windmill-lite selfcheck: the expression engine does not start: %v\n", err)
		return 1
	}
	defer e.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	failed := 0
	for _, p := range selfcheckProbes {
		prog, err := e.Compile(p.source)
		var got []byte
		if err == nil {
			got, err = e.Eval(ctx, prog, expr.Input{Strings: p.input})
		}
		switch {
		case err != nil:
			fmt.Fprintf(stderr, "windmill-lite selfcheck: %s: %v\n", p.name, err)
			failed++
		case string(got) != p.want:
			fmt.Fprintf(stderr, "windmill-lite selfcheck: %s: got %s, want %s\n", p.name, got, p.want)
			failed++
		}
	}
	if failed > 0 {
		return 1
	}
	fmt.Fprintf(stdout, "windmill-lite %s (%s/%s): expression engine ok, %d probes in %v\n",
		resolveVersion(), runtime.GOOS, runtime.GOARCH, len(selfcheckProbes), time.Since(start).Round(time.Millisecond))
	return 0
}
