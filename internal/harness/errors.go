package harness

import (
	"errors"
	"fmt"
)

// ProtocolError is a violation of the harness protocol by the peer, or a
// message this side refuses to send. Code is one of the Code constants; the
// same codes are used by every harness's codec and by the shared cases in
// conformance/protocol/frames.json.
//
// A peer that violates the protocol cannot be trusted to keep its
// invocations apart, so a Process that receives one kills its harness
// (ADR 0004: the process's in-flight invocations then fail as escaped).
type ProtocolError struct {
	Code    string
	Message string
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("harness protocol: %s: %s", e.Code, e.Message)
}

func protoErr(code, format string, args ...any) *ProtocolError {
	return &ProtocolError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// IsProtocolError reports whether err is a *ProtocolError with the given
// code, or with any code when code is empty.
func IsProtocolError(err error, code string) bool {
	var pe *ProtocolError
	return errors.As(err, &pe) && (code == "" || pe.Code == code)
}

// Protocol error codes.
const (
	// CodeFrameEmpty: a frame of length 0, which cannot hold an object.
	CodeFrameEmpty = "frame_empty"
	// CodeFrameTooLarge: a frame longer than the maximum frame payload
	// (1 MiB); a longer message must be sent as part frames.
	CodeFrameTooLarge = "frame_too_large"
	// CodeTruncated: the stream ended inside a frame.
	CodeTruncated = "truncated"
	// CodeInvalidUTF8: a payload, or a string to encode, that is not UTF-8.
	CodeInvalidUTF8 = "invalid_utf8"
	// CodeInvalidJSON: a payload that is not one JSON value.
	CodeInvalidJSON = "invalid_json"
	// CodeNotObject: a payload that is JSON but not an object.
	CodeNotObject = "not_object"
	// CodeMissingType: an object without a string `t`.
	CodeMissingType = "missing_type"
	// CodeUnknownType: a `t` this side does not implement.
	CodeUnknownType = "unknown_type"
	// CodeMissingField: a required field is absent.
	CodeMissingField = "missing_field"
	// CodeBadField: a field has the wrong JSON type or a value outside its
	// enumeration.
	CodeBadField = "bad_field"
	// CodePartSequence: a part frame out of order: a stream that does not
	// start at seq 0, a gap or repeat, or a seq 0 for a stream that is
	// already open.
	CodePartSequence = "part_sequence"
	// CodePartInPart: a part stream that reassembles to another part frame.
	CodePartInPart = "part_in_part"
	// CodeBadChunk: a part chunk that is not padded standard base64, or
	// is empty.
	CodeBadChunk = "bad_chunk"
	// CodeMessageTooLarge: a message larger than the maximum message size
	// (512 MiB unless lowered in the handshake).
	CodeMessageTooLarge = "message_too_large"
	// CodeUnexpected: a message this side does not accept from its peer in
	// the current state: one sent in the wrong direction, before the
	// handshake, or about an invocation in the wrong state.
	CodeUnexpected = "unexpected_message"
)
