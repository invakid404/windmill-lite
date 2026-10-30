package oracle

import (
	"bytes"
	"io"
	"strings"
)

// RedactedToken takes the place of the session's bearer token in everything
// a session reads from the oracle. The token authenticates the throwaway
// superadmin of the session's oracle, and Windmill hands it on: a flow
// started over HTTP carries the request's headers, Authorization included,
// in its trigger event, so a preprocessor's arguments hold the token. A
// session replaces the token where data enters it: in every response body
// and Content-Type header of the API client, in the effects and stub calls
// that scripts post, and in the CLI's output. So no recording, raw capture, report, log line or
// error message holds it, whatever a fixture records or masks.
const RedactedToken = "<redacted-token>"

// redactToken returns data with every occurrence of token replaced by
// RedactedToken. An empty token changes nothing.
func redactToken(data []byte, token string) []byte {
	if token == "" || !bytes.Contains(data, []byte(token)) {
		return data
	}
	return bytes.ReplaceAll(data, []byte(token), []byte(RedactedToken))
}

// redactTokenString is redactToken for a string.
func redactTokenString(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, RedactedToken)
}

// redactingWriter writes to w with every occurrence of token replaced by
// RedactedToken. A token holds no line break, so the writer passes on whole
// lines and keeps the unfinished last line until the next write or Flush;
// a token split across two writes is still replaced.
type redactingWriter struct {
	w       io.Writer
	token   string
	pending []byte
}

func (r *redactingWriter) Write(p []byte) (int, error) {
	r.pending = append(r.pending, p...)
	if i := bytes.LastIndexByte(r.pending, '\n'); i >= 0 {
		line := redactToken(r.pending[:i+1], r.token)
		r.pending = append([]byte(nil), r.pending[i+1:]...)
		if _, err := r.w.Write(line); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

// Flush writes the unfinished last line.
func (r *redactingWriter) Flush() error {
	if len(r.pending) == 0 {
		return nil
	}
	_, err := r.w.Write(redactToken(r.pending, r.token))
	r.pending = nil
	return err
}
