// This file ports pglz_compress from PostgreSQL 16
// (src/common/pg_lzcompress.c), which is under the PostgreSQL License:
//
// Portions Copyright (c) 1996-2026, PostgreSQL Global Development Group
// Portions Copyright (c) 1994, The Regents of the University of California
//
// The full notice is in LICENSE.PostgreSQL beside this file.

package jsonval

// pglz's default strategy (PGLZ_strategy_default) and limits.
const (
	pglzMinInput       = 32   // inputs shorter than this are not compressed
	pglzMinCompRate    = 25   // the output must save at least 25 %
	pglzFirstSuccessBy = 1024 // give up if no match is found in the first 1 KiB of output
	pglzMatchSizeGood  = 128  // stop looking once a match this long is found
	pglzMatchSizeDrop  = 10   // and accept 10 % less at each entry visited
	pglzMaxMatch       = 273
	pglzHistorySize    = 4096
)

// pglzEntry is one entry of pglz's history: a position of the input, in
// the list of its hash slot. prev is -1 for the head of its list; entry 0
// is the lists' end, whose fields are written but never read.
type pglzEntry struct {
	next, prev, hindex, pos int
}

// pglzCompressedLen is the length of what pglz_compress writes for src
// with the default strategy, or -1 where it fails: an input shorter than
// 32 bytes, an output that would not save 25 %, or no match within the
// first 1024 bytes of output. It reproduces the C code step for step,
// counting the output instead of writing it. The hash reads the input as
// C's signed char, as on x86-64, where the pinned oracle's PostgreSQL runs.
func pglzCompressedLen(src []byte) int {
	slen := len(src)
	if slen < pglzMinInput {
		return -1
	}
	var resultMax int
	if slen > (1<<31-1)/100 {
		resultMax = (slen / 100) * (100 - pglzMinCompRate)
	} else {
		resultMax = slen * (100 - pglzMinCompRate) / 100
	}
	var hashsz int
	switch {
	case slen < 128:
		hashsz = 512
	case slen < 256:
		hashsz = 1024
	case slen < 512:
		hashsz = 2048
	case slen < 1024:
		hashsz = 4096
	default:
		hashsz = 8192
	}
	mask := hashsz - 1
	histStart := make([]int, hashsz)
	entries := make([]pglzEntry, pglzHistorySize+1)
	histNext, recycle := 1, false

	idx := func(s int) int {
		if slen-s < 4 {
			return int(int8(src[s])) & mask
		}
		return ((int(int8(src[s])) << 6) ^ (int(int8(src[s+1])) << 4) ^ (int(int8(src[s+2])) << 2) ^ int(int8(src[s+3]))) & mask
	}
	add := func(s int) {
		h := idx(s)
		my := &entries[histNext]
		if recycle {
			if my.prev == -1 {
				histStart[my.hindex] = my.next
			} else {
				entries[my.prev].next = my.next
			}
			entries[my.next].prev = my.prev
		}
		my.next, my.prev, my.hindex, my.pos = histStart[h], -1, h, s
		entries[histStart[h]].prev = histNext
		histStart[h] = histNext
		if histNext++; histNext >= pglzHistorySize+1 {
			histNext, recycle = 1, true
		}
	}
	find := func(dp int) (int, bool) {
		good := pglzMatchSizeGood
		length := 0
		for h := histStart[idx(dp)]; h != 0; {
			ip, hp := dp, entries[h].pos
			if ip-hp >= 0x0fff {
				break
			}
			n := 0
			if length >= 16 {
				// memcmp over the best length so far; past the input's
				// end no candidate can be longer, so it is skipped.
				if ip+length <= slen && string(src[ip:ip+length]) == string(src[hp:hp+length]) {
					n = length
					ip, hp = ip+length, hp+length
					for ip < slen && src[ip] == src[hp] && n < pglzMaxMatch {
						n, ip, hp = n+1, ip+1, hp+1
					}
				}
			} else {
				for ip < slen && src[ip] == src[hp] && n < pglzMaxMatch {
					n, ip, hp = n+1, ip+1, hp+1
				}
			}
			if n > length {
				length = n
			}
			if h = entries[h].next; h != 0 {
				if length >= good {
					break
				}
				good -= good * pglzMatchSizeDrop / 100
			}
		}
		return length, length > 2
	}

	out := 0
	var ctrl uint8
	found := false
	for dp := 0; dp < slen; {
		if out >= resultMax || (!found && out >= pglzFirstSuccessBy) {
			return -1
		}
		if ctrl == 0 {
			out++ // a control byte for the next eight items
			ctrl = 1
		}
		ctrl <<= 1
		if n, ok := find(dp); ok {
			if n > 17 {
				out += 3
			} else {
				out += 2
			}
			for ; n > 0; n-- {
				add(dp)
				dp++
			}
			found = true
		} else {
			out++
			add(dp)
			dp++
		}
	}
	if out >= resultMax {
		return -1
	}
	return out
}
