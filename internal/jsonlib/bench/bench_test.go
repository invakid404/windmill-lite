package bench

import (
	"os"
	"strings"
	"testing"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// BenchmarkLib runs every operation for every candidate, family and size:
// BenchmarkLib/size=S/family=F/op=O/cand=C, the candidates of one case run
// one after another. JSONLIB_SIZES (labels such as 1KiB,4MiB) narrows the
// sizes; -bench narrows the rest. ns/op and MB/s are per payload byte.
func BenchmarkLib(b *testing.B) {
	for _, n := range sizes(b) {
		b.Run("size="+jsonval.SizeLabel(n), func(b *testing.B) {
			for _, f := range Families {
				b.Run("family="+f, func(b *testing.B) { runOps(b, Make(f, n)) })
			}
		})
	}
}

// BenchmarkBrief runs every operation for every candidate on the workload
// brief's families at their typical, p90 and worst sizes:
// BenchmarkBrief/family=F/tier=T/op=O/cand=C.
func BenchmarkBrief(b *testing.B) {
	for _, f := range Brief {
		b.Run("family="+f.Name, func(b *testing.B) {
			for _, t := range f.Tiers {
				b.Run("tier="+t.Name, func(b *testing.B) { runOps(b, f.Make(t.Size)) })
			}
		})
	}
}

// runOps runs each operation on payload, each candidate in turn.
func runOps(b *testing.B, payload []byte) {
	for _, op := range Ops {
		b.Run("op="+op.Name, func(b *testing.B) {
			for _, c := range Cands {
				if !op.Has(c) {
					continue
				}
				b.Run("cand="+c.Name, func(b *testing.B) {
					in, err := op.Prepare(c, payload)
					if err != nil {
						b.Fatalf("prepare: %v", err)
					}
					b.SetBytes(int64(len(payload)))
					b.ReportAllocs()
					for b.Loop() {
						if _, err := op.Do(c, in); err != nil {
							b.Fatal(err)
						}
					}
				})
			}
		})
	}
}

// sizes are Sizes, or the labels JSONLIB_SIZES lists.
func sizes(tb testing.TB) []int {
	env := os.Getenv("JSONLIB_SIZES")
	if env == "" {
		return Sizes
	}
	var out []int
	for _, l := range strings.Split(env, ",") {
		n, ok := ParseSize(l)
		if !ok {
			tb.Fatalf("JSONLIB_SIZES: bad size %q", l)
		}
		out = append(out, n)
	}
	return out
}
