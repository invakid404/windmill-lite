package wl_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/invakid404/windmill-lite/wl"
)

// This example is the one in the package README: it opens a bundle, runs
// its flow f/app/main and prints the run's status and result. It has no
// Output comment, so go test compiles it and does not run it: it needs a
// built bundle, Bun 1.4.3 and the harness runtime of the same version.
func Example() {
	rt, err := wl.Open("fixture.wlb", wl.Options{
		Bun:            "/path/to/bun-1.4.3/bun",
		HarnessRuntime: "windmill-lite/harnesses/bun/runtime",
	})
	if err != nil {
		log.Fatal(err)
	}
	defer rt.Close()

	args := json.RawMessage(`{"name": "lite"}`)
	res, err := rt.Run(context.Background(), "f/app/main", args, wl.RunOptions{})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.Status, string(res.Result))
}
