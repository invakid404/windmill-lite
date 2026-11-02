package wl

import (
	"encoding/json"
	"maps"
	"slices"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// spliced is a value's stored text in a host_call payload (ADR 0008): JSON
// text that the engine canonicalised, so valid, compact and in stored
// form, which marshalPayload writes into the payload as it is.
// encoding/json would check it, copy it compact and escape <, >, &,
// U+2028 and U+2029 in its strings again; the plugin's JSON.parse reads
// the same values either way. Under encoding/json it marshals as a
// json.RawMessage does.
type spliced []byte

func (s spliced) MarshalJSON() ([]byte, error) {
	if s == nil {
		return []byte("null"), nil
	}
	return s, nil
}

// payloadText is a value as a payload carries it: its stored text, spliced,
// or its producer text when it has none (a text jsonb refuses), which
// encoding/json writes as it writes a json.RawMessage, or nil for the zero
// value.
func payloadText(v jsonval.Value) any {
	if v.IsZero() {
		return nil
	}
	if s, err := v.Stored(); err == nil {
		return spliced(s)
	}
	return json.RawMessage(v.Producer())
}

// marshalPayload writes a host_call payload: json.Marshal's text, except
// that the stored texts it holds (spliced) are written as they are. Maps
// are written with their keys sorted, as encoding/json sorts them, and
// every other value by encoding/json itself, so the bytes differ from
// json.Marshal's only in the escapes encoding/json would add inside a
// stored text. When encoding/json fails on a part, the payload is
// marshalled whole by it, so the error is json.Marshal's.
func marshalPayload(payload map[string]any) ([]byte, error) {
	b, err := appendPayload(make([]byte, 0, payloadSize(payload)), payload)
	if err != nil {
		return json.Marshal(payload)
	}
	return b, nil
}

func appendPayload(b []byte, v any) ([]byte, error) {
	switch x := v.(type) {
	case spliced:
		if x == nil {
			return append(b, "null"...), nil
		}
		return append(b, x...), nil
	case map[string]any:
		if x == nil {
			return append(b, "null"...), nil
		}
		b = append(b, '{')
		for i, k := range slices.Sorted(maps.Keys(x)) {
			if i > 0 {
				b = append(b, ',')
			}
			kb, err := json.Marshal(k)
			if err != nil {
				return nil, err
			}
			b = append(append(b, kb...), ':')
			if b, err = appendPayload(b, x[k]); err != nil {
				return nil, err
			}
		}
		return append(b, '}'), nil
	case []any:
		if x == nil {
			return append(b, "null"...), nil
		}
		b = append(b, '[')
		for i, e := range x {
			if i > 0 {
				b = append(b, ',')
			}
			var err error
			if b, err = appendPayload(b, e); err != nil {
				return nil, err
			}
		}
		return append(b, ']'), nil
	}
	t, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(b, t...), nil
}

// payloadSize is about the size of the payload's text, so that it is
// written into one buffer: its spliced texts, and a little for the rest.
func payloadSize(v any) int {
	switch x := v.(type) {
	case spliced:
		return len(x)
	case json.RawMessage:
		return len(x)
	case map[string]any:
		n := 2
		for k, e := range x {
			n += len(k) + 4 + payloadSize(e)
		}
		return n
	case []any:
		n := 2
		for _, e := range x {
			n += 1 + payloadSize(e)
		}
		return n
	}
	return 16
}
