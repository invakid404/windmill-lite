// Package bench holds what the repository's benchmarks share (ADR 0005,
// the boundary benchmarks): the synthetic payloads they all measure, the
// nearest-rank percentile they report, and a runner that starts workers
// together and reports the concurrency they reached. Only test files use
// it.
package bench

import (
	"strconv"
	"strings"
)

// SyntheticMin is the smallest payload Synthetic makes, the 26-byte object
// {"id":1,"name":"step-one"}: the small end of the range ADR 0005's
// benchmark plan measures.
const SyntheticMin = 26

// SyntheticSizes are the payload sizes the boundary benchmarks measure,
// from 26 B to 4 MiB: a small value, values that fit one frame, and
// values that travel in part frames (above 1 MiB, ADR 0004).
var SyntheticSizes = []int{SyntheticMin, 1 << 10, 4 << 10, 32 << 10, 256 << 10, 1 << 20, 4 << 20}

// SizeLabel names a payload size in benchmark names: 26B, 4KiB, 1MiB.
func SizeLabel(n int) string {
	switch {
	case n >= 1<<20 && n%(1<<20) == 0:
		return strconv.Itoa(n>>20) + "MiB"
	case n >= 1<<10 && n%(1<<10) == 0:
		return strconv.Itoa(n>>10) + "KiB"
	}
	return strconv.Itoa(n) + "B"
}

// Synthetic returns a compact JSON object text of exactly size bytes shaped
// like a step's result: {"items":[...],"pad":"..."}, as many records as
// fit, each holding strings (one with an escaped newline), an integer, a
// decimal, a boolean, a list, a nested object and null, then a padding
// string that brings the text to size. A size below 64 bytes gives the
// 26-byte object of SyntheticMin. The text is valid UTF-8 and holds no
// NUL; the same size always gives the same text, so the benchmarks of
// every layer (the canonicaliser, the frame codecs, the transport and
// whole runs) measure the same bytes.
func Synthetic(size int) []byte {
	if size < 64 {
		return []byte(`{"id":1,"name":"step-one"}`)
	}
	const tail = `],"pad":""}`
	var b strings.Builder
	b.Grow(size)
	b.WriteString(`{"items":[`)
	var rec []byte
	for i := 0; ; i++ {
		rec = rec[:0]
		if i > 0 {
			rec = append(rec, ',')
		}
		rec = append(rec, `{"id":`...)
		rec = strconv.AppendInt(rec, int64(i), 10)
		rec = append(rec, `,"name":"item `...)
		rec = strconv.AppendInt(rec, int64(100000+i), 10)
		rec = append(rec, `","note":"line one\nline two","tags":["alpha","beta"],"price":`...)
		rec = strconv.AppendInt(rec, int64(i%1000), 10)
		rec = append(rec, `.25,"active":true,"meta":{"zone":"eu-1","score":0.5,"ref":null}}`...)
		if b.Len()+len(rec)+len(tail) > size {
			break
		}
		b.Write(rec)
	}
	b.WriteString(`],"pad":"`)
	b.WriteString(strings.Repeat("x", size-b.Len()-2))
	b.WriteString(`"}`)
	return []byte(b.String())
}
