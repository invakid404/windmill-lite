package syncfolder

import "strings"

// HasNativeAnnotation reports whether TypeScript source carries the
// `native` header annotation, which makes a Bun script `bunnative` when it
// is deployed (SY-3, SY-V3). It is HasAnnotation(src, "native").
func HasNativeAnnotation(src string) bool { return HasAnnotation(src, "native") }

// HasAnnotation reports whether TypeScript source carries the header
// annotation key, by the rules Windmill reads every leading-comment
// annotation with (SY-V3, WR-31):
//
//   - Lines are split at `\n`, with a `\r` before it dropped. Blank lines
//     (empty after trimming Unicode white space) are skipped.
//   - The header is the run of lines that start exactly with `//`; the
//     first other line ends it, including an indented `//` line.
//   - A header comment's key is its text after `//`, trimmed, then cut at
//     the first `=`. The key matches case-sensitively, whatever follows the
//     `=`; `native =true` has the key `native ` and is not `native`.
func HasAnnotation(src, key string) bool {
	for line := range strings.SplitSeq(src, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		text, ok := strings.CutPrefix(line, "//")
		if !ok {
			return false
		}
		if k, _, _ := strings.Cut(strings.TrimSpace(text), "="); k == key {
			return true
		}
	}
	return false
}
