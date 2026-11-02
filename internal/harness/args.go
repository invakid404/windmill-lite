package harness

import (
	"bytes"

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
// are kept as they are, for the harness to refuse: the object must close
// with its own brace and nothing may follow it, or a malformed text whose
// internal key was dropped would come out valid.
func scriptArgs(args jsonval.Value) jsonval.Value {
	text := args.Producer()
	// A key is an internal one only when the text spells it, as itself or
	// with \u escapes: no other JSON escape writes a letter or an
	// underscore. Without either, nothing is dropped and the arguments
	// stay as they are.
	if !bytes.Contains(text, []byte("_MODULES")) && !bytes.Contains(text, []byte("_TEMP_SCRIPT_REFS")) && !bytes.Contains(text, []byte(`\u`)) {
		return args
	}
	return walkedArgs(args)
}

// walkedArgs is scriptArgs' walk: the arguments without their internal
// members, or the arguments as they are when none is there or the text is
// not one object.
func walkedArgs(args jsonval.Value) jsonval.Value {
	text := args.Producer()
	var kept [][]byte
	dropped := false
	rest, ok := jsonval.Members(text, func(key string, ks, _, _, ve int) {
		if internalArgs[key] {
			dropped = true
			return
		}
		// The member's text from its key to its value's end.
		kept = append(kept, text[ks:ve])
	})
	if !ok || rest != jsonval.RestNone || !dropped {
		return args
	}
	out := append([]byte{'{'}, bytes.Join(kept, []byte(","))...)
	return jsonval.FromProducer(append(out, '}'))
}
