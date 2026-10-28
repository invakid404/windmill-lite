package openflow

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ScriptHash pins a deployed script version (`script.hash`, OF-14). It is a
// signed 64-bit integer, written as 16 lowercase hex digits.
type ScriptHash int64

// ParseScriptHash decodes a hash with Windmill's rules (OF-14): an even
// number of hex digits in either case that decodes to at least 8 bytes, of
// which the first 8, read big-endian, are the hash. Extra bytes are ignored.
func ParseScriptHash(s string) (ScriptHash, error) {
	if len(s)%2 != 0 {
		return 0, errors.New("Odd number of digits")
	}
	b := make([]byte, len(s)/2)
	for i := range len(s) {
		v, ok := hexDigit(s[i])
		if !ok {
			return 0, fmt.Errorf("Invalid character %q at position %d", rune(s[i]), i)
		}
		b[i/2] = b[i/2]<<4 | v
	}
	if len(b) < 8 {
		return 0, fmt.Errorf("hex string did not decode to an u64: %s", s)
	}
	return ScriptHash(binary.BigEndian.Uint64(b)), nil
}

// String is the hash's serialisation: always 16 lowercase hex digits.
func (h ScriptHash) String() string {
	return fmt.Sprintf("%016x", uint64(h))
}

func hexDigit(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
