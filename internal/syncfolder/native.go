package syncfolder

import "strings"

// HasNativeAnnotation reports whether TypeScript source carries the
// `native` header annotation, which makes a Bun script `bunnative` when it
// is deployed (SY-3, SY-V3):
//
//   - Lines are split at `\n`, with a `\r` before it dropped. Blank lines
//     (empty after trimming Unicode white space) are skipped.
//   - The header is the run of lines that start exactly with `//`; the
//     first other line ends it, including an indented `//` line.
//   - A header comment's key is its text after `//`, trimmed, then cut at
//     the first `=`. The key `native` (case-sensitive) enables the
//     annotation, whatever follows the `=`; `native =true` has the key
//     `native ` and does not.
func HasNativeAnnotation(src string) bool {
	for line := range strings.SplitSeq(src, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		text, ok := strings.CutPrefix(line, "//")
		if !ok {
			return false
		}
		if key, _, _ := strings.Cut(strings.TrimSpace(text), "="); key == "native" {
			return true
		}
	}
	return false
}
