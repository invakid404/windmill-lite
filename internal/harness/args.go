package harness

import (
	"bytes"
	"encoding/json"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// internalArgs are the top-level keys of a job's arguments that Windmill's
// worker removes from args.json before the script runs (WR-5); every other
// key stays, underscore keys such as _ENTRYPOINT_OVERRIDE included.
var internalArgs = map[string]bool{"_MODULES": true, "_TEMP_SCRIPT_REFS": true}

// scriptArgs is what a step's script receives, and what args.json holds:
// the invocation's argument object without its internalArgs (WR-5), every
// other member kept as its raw text, in its order. Windmill's wrapper
// binds the script's parameters from args.json, so the invoke frame
// carries the same text. Arguments that are not an object, or not JSON,
// are kept as they are, for the harness to refuse.
func scriptArgs(args jsonval.Value) jsonval.Value {
	text := args.Producer()
	// A key can only match with an underscore or an escape in its text.
	if !bytes.Contains(text, []byte("_MODULES")) && !bytes.Contains(text, []byte("_TEMP_SCRIPT_REFS")) && !bytes.Contains(text, []byte(`\`)) {
		return args
	}
	dec := json.NewDecoder(bytes.NewReader(text))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return args
	}
	var kept [][]byte
	dropped := false
	for dec.More() {
		start := dec.InputOffset()
		t, err := dec.Token()
		key, ok := t.(string)
		if err != nil || !ok {
			return args
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return args
		}
		if internalArgs[key] {
			dropped = true
			continue
		}
		// The member's text from its key to its value's end, without the
		// comma and the whitespace before it.
		m := bytes.TrimLeft(text[start:dec.InputOffset()], " \t\r\n")
		kept = append(kept, bytes.TrimLeft(bytes.TrimPrefix(m, []byte(",")), " \t\r\n"))
	}
	if !dropped {
		return args
	}
	out := append([]byte{'{'}, bytes.Join(kept, []byte(","))...)
	return jsonval.FromProducer(append(out, '}'))
}
