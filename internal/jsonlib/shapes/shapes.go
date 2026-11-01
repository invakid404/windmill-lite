// Package shapes makes the JSON-library study's synthetic payloads: eight
// generic families from 1 KiB to 64 MiB, and the workload brief's families
// F1 to F8 at their typical, p90 and worst sizes (branch
// study/json-libraries, never merged). It depends on no JSON library, so
// that end-to-end benchmarks can use it with any of them.
package shapes

import (
	"encoding/base64"
	"fmt"
	"math"
	"math/rand/v2"
	"strconv"
	"strings"

	"github.com/invakid404/windmill-lite/internal/bench"
)

// Families are the payload shapes, by name. Each makes a compact JSON
// object of exactly the size asked for (at least 1 KiB), the same bytes for
// the same size, valid UTF-8 and without \u0000, the last member a padding
// string that brings it to size.
var Families = []string{"records", "strings", "floats", "base64", "deep", "wide", "mixed", "synthetic"}

// Make returns family's payload of size bytes.
func Make(family string, size int) []byte {
	switch family {
	case "records":
		return list(size, "items", records())
	case "strings":
		return list(size, "texts", texts(size))
	case "floats":
		return list(size, "vectors", vectors(size))
	case "base64":
		return list(size, "files", blobs(size))
	case "deep":
		return list(size, "trees", trees(size))
	case "wide":
		return wide(size)
	case "mixed":
		return mixed(size)
	case "synthetic":
		return bench.Synthetic(size)
	}
	panic("bench: no family " + family)
}

// item appends the i-th element of a list.
type item func(b []byte, i int) []byte

// list is {"<field>":[item 0, item 1, ...],"pad":"xx..."} of exactly size
// bytes: as many whole items as fit, then the padding.
func list(size int, field string, it item) []byte {
	head := `{"` + field + `":[`
	const tail = `],"pad":""}`
	b := make([]byte, 0, size)
	b = append(b, head...)
	var next []byte
	for i := 0; ; i++ {
		next = next[:0]
		if i > 0 {
			next = append(next, ',')
		}
		next = it(next, i)
		if len(b)+len(next)+len(tail) > size {
			break
		}
		b = append(b, next...)
	}
	return pad(b, size, `],"pad":"`)
}

// pad closes b with open, a padding string and `"}`, to exactly size bytes.
func pad(b []byte, size int, open string) []byte {
	b = append(b, open...)
	n := size - len(b) - 2
	if n < 0 {
		panic(fmt.Sprintf("bench: %d bytes do not fit in %d", len(b)+2, size))
	}
	b = append(b, strings.Repeat("x", n)...)
	return append(b, `"}`...)
}

// records are small records of mixed scalars, an array and a nested
// object: what most step results look like.
func records() item {
	return func(b []byte, i int) []byte {
		b = append(b, `{"id":`...)
		b = strconv.AppendInt(b, int64(i), 10)
		b = append(b, `,"name":"user-`...)
		b = appendPadded(b, i)
		b = append(b, `","email":"user`...)
		b = strconv.AppendInt(b, int64(i), 10)
		b = append(b, `@example.invalid","age":`...)
		b = strconv.AppendInt(b, int64(18+i%60), 10)
		b = append(b, `,"score":`...)
		b = strconv.AppendInt(b, int64(i%100), 10)
		b = append(b, `.25,"active":`...)
		b = strconv.AppendBool(b, i%3 != 0)
		b = append(b, `,"tags":["red","green"],"address":{"city":"Springfield","zip":"`...)
		b = appendPadded(b, i%100000)
		return append(b, `"},"manager":null}`...)
	}
}

func appendPadded(b []byte, n int) []byte {
	s := strconv.Itoa(n)
	for range 6 - len(s) {
		b = append(b, '0')
	}
	return append(b, s...)
}

// textChunk is one piece of a long string as JSON.stringify writes it:
// escapes for quotes, backslashes and control characters, everything else
// (accents, CJK, an emoji, U+2028 and U+2029, HTML-significant characters)
// as raw UTF-8.
const textChunk = "Lorem ipsum dolor sit amet, \\\"quoted\\\" and back\\\\slashed;\\nsecond line\\twith a tab, " +
	"caf\\u00e9 is spelled caf\u00e9, na\u00efve fa\u00e7ade, Gr\u00fc\u00dfe, \u6771\u4eac\u90fd, emoji \U0001F600\U0001F680, " +
	"<b>bold</b> & <i>italic</i>, a line separator \u2028 and a paragraph separator \u2029 end.\\r\\n"

// texts are strings of about 4 KiB, or an eighth of size when that is less.
func texts(size int) item {
	long := strings.Repeat(textChunk, 4096/len(textChunk)+1)
	if n := max(size/8, 64); n < len(long) {
		// Cut at a rune boundary and not inside an escape.
		long = strings.ToValidUTF8(long[:n], "")
		if i := strings.LastIndexByte(long, ' '); i > 0 {
			long = long[:i]
		}
	}
	return func(b []byte, i int) []byte {
		b = append(b, `"#`...)
		b = strconv.AppendInt(b, int64(i), 10)
		b = append(b, ' ')
		b = append(b, long...)
		return append(b, '"')
	}
}

// vectors are embedding vectors: 768 float32 values in [-0.2, 0.2) (fewer
// when size is below 64 KiB), each in its shortest float32 spelling, as
// embedding APIs print them.
func vectors(size int) item {
	r := rand.New(rand.NewPCG(1, 2))
	dims := min(768, max(size/96, 4))
	return func(b []byte, i int) []byte {
		b = append(b, '[')
		for j := range dims {
			if j > 0 {
				b = append(b, ',')
			}
			b = appendJSNumber(b, float64(float32(r.Float64()*0.4-0.2)), 32)
		}
		return append(b, ']')
	}
}

// appendJSNumber writes f as JavaScript's Number#toString writes it: plain
// decimal from 1e-7 to below 1e21, exponent notation outside it, with the
// shortest digits of a float of bits bits.
func appendJSNumber(b []byte, f float64, bits int) []byte {
	if a := math.Abs(f); a == 0 || a >= 1e-7 && a < 1e21 {
		return strconv.AppendFloat(b, f, 'f', -1, bits)
	}
	s := strconv.FormatFloat(f, 'e', -1, bits)
	mant, exp, _ := strings.Cut(s, "e")
	sign := exp[0]
	exp = strings.TrimLeft(exp[1:], "0")
	b = append(b, mant...)
	b = append(b, 'e', sign)
	return append(b, exp...)
}

// blobs are files of 48 KiB of random bytes (an eighth of size when that
// is less), base64-encoded.
func blobs(size int) item {
	r := rand.New(rand.NewPCG(3, 4))
	raw := make([]byte, min(48<<10, max(size/8, 16)))
	return func(b []byte, i int) []byte {
		for j := range raw {
			raw[j] = byte(r.Uint32())
		}
		b = append(b, `{"name":"blob-`...)
		b = appendPadded(b, i)
		b = append(b, `.bin","type":"application/octet-stream","data":"`...)
		b = base64.StdEncoding.AppendEncode(b, raw)
		return append(b, `"}`...)
	}
}

// trees are objects nested 64 deep (less when size is below 16 KiB), each
// level with a counter and a short array; with the wrapper the text is 66
// levels deep.
func trees(size int) item {
	depth := min(64, max(size/256, 2))
	return func(b []byte, i int) []byte {
		for d := range depth {
			b = append(b, `{"n":`...)
			b = strconv.AppendInt(b, int64(i*64+d), 10)
			b = append(b, `,"p":[`...)
			b = strconv.AppendInt(b, int64(d), 10)
			b = append(b, `,"x"],"c":`...)
		}
		b = append(b, `{"leaf":true}`...)
		for range depth {
			b = append(b, '}')
		}
		return b
	}
}

// wide is one object with many members, keys k000000 on, values cycling
// through a string, an integer, a decimal, a boolean, null and a small
// object.
func wide(size int) []byte {
	b := make([]byte, 0, size)
	b = append(b, '{')
	var next []byte
	const tail = `,"pad":""}`
	for i := 0; ; i++ {
		next = next[:0]
		if i > 0 {
			next = append(next, ',')
		}
		next = append(next, `"k`...)
		next = appendPadded(next, i)
		next = append(next, `":`...)
		switch i % 6 {
		case 0:
			next = append(next, `"value `...)
			next = strconv.AppendInt(next, int64(i), 10)
			next = append(next, '"')
		case 1:
			next = strconv.AppendInt(next, int64(i), 10)
		case 2:
			next = strconv.AppendInt(next, int64(i), 10)
			next = append(next, ".5"...)
		case 3:
			next = append(next, "true"...)
		case 4:
			next = append(next, "null"...)
		case 5:
			next = append(next, `{"a":1,"b":"two"}`...)
		}
		if len(b)+len(next)+len(tail) > size {
			break
		}
		b = append(b, next...)
	}
	return pad(b, size, `,"pad":"`)
}

// mixed is a step result that holds a little of everything: records, long
// strings, vectors, blobs, trees and a wide object, each about a sixth of
// the size, as members of one object.
func mixed(size int) []byte {
	part := (size - 200) / 6
	b := make([]byte, 0, size)
	b = append(b, '{')
	for i, p := range []struct {
		name string
		text []byte
	}{
		{"records", list(part, "items", records())},
		{"texts", list(part, "texts", texts(part))},
		{"vectors", list(part, "vectors", vectors(part))},
		{"files", list(part, "files", blobs(part))},
		{"trees", list(part, "trees", trees(part))},
		{"attrs", wide(part)},
	} {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '"')
		b = append(b, p.name...)
		b = append(b, `":`...)
		b = append(b, p.text...)
	}
	return pad(b, size, `,"pad":"`)
}

// Sizes are the payload sizes the microbenchmarks measure: 1 KiB to
// 64 MiB, dense from 1 to 16 MiB.
var Sizes = []int{1 << 10, 16 << 10, 256 << 10, 1 << 20, 2 << 20, 3 << 20, 4 << 20, 6 << 20, 8 << 20, 12 << 20, 16 << 20, 32 << 20, 64 << 20}

// ParseSize reads a size label as bench.SizeLabel writes it.
func ParseSize(l string) (int, bool) {
	for _, u := range []struct {
		suffix string
		mul    int
	}{{"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}} {
		if v, ok := strings.CutSuffix(l, u.suffix); ok {
			n, err := strconv.Atoi(v)
			return n * u.mul, err == nil && n > 0
		}
	}
	return 0, false
}
