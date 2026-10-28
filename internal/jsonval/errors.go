package jsonval

import (
	"errors"
	"fmt"
)

// The reasons a value has no stored form. Stored reports each as the Kind of
// a *StoreError, so errors.Is(err, ErrNUL) and the like work on its result.
var (
	// ErrNUL: the text holds the escape \u0000 in a string or a key, which
	// PostgreSQL's jsonb input rejects (OR-27, FX-48).
	ErrNUL = errors.New(`jsonval: value holds \u0000, which jsonb rejects`)

	// ErrUnpairedSurrogate: the text holds a UTF-16 surrogate escape that is
	// not half of a high-low pair, which jsonb input also rejects. The
	// reference states only the NUL case; this one is PostgreSQL's
	// behaviour, checked against PostgreSQL in CI.
	ErrUnpairedSurrogate = errors.New("jsonval: value holds a surrogate escape without its pair, which jsonb rejects")

	// ErrNumericOverflow: the text holds a number outside the range of
	// PostgreSQL numeric (more than 131072 digits before the decimal point,
	// more than 16383 after it, or an exponent beyond 2^30-1 in magnitude),
	// which jsonb input rejects. Like ErrUnpairedSurrogate, this is
	// PostgreSQL's behaviour, not a reference finding.
	ErrNumericOverflow = errors.New("jsonval: value holds a number outside the range of PostgreSQL numeric, which jsonb rejects")

	// ErrSyntax: the text is not exactly one JSON value in valid UTF-8.
	// Producers always write valid JSON, so this marks a programming error
	// upstream rather than a Windmill behaviour.
	ErrSyntax = errors.New("jsonval: text is not one JSON value")
)

// A StoreError reports why a value has no stored form: the first token of
// its producer text that PostgreSQL's jsonb input rejects.
type StoreError struct {
	// Kind is ErrNUL, ErrUnpairedSurrogate, ErrNumericOverflow or ErrSyntax.
	Kind error
	// Offset is the byte offset in the producer text of the rejected
	// escape, number or character.
	Offset int

	detail string
}

func (e *StoreError) Error() string {
	if e.detail != "" {
		return fmt.Sprintf("%v: %s at byte %d", e.Kind, e.detail, e.Offset)
	}
	return fmt.Sprintf("%v (at byte %d)", e.Kind, e.Offset)
}

func (e *StoreError) Unwrap() error { return e.Kind }

// DatabaseMessage returns PostgreSQL's primary error message for the
// rejection: the text that follows "error returned from database: " in the
// error Windmill reports when storing the value fails (OR-27 records it for
// NUL). The routing and the rest of that error are the engine's (FX-48).
func (e *StoreError) DatabaseMessage() string {
	switch e.Kind {
	case ErrNUL:
		return "unsupported Unicode escape sequence"
	case ErrNumericOverflow:
		return "value overflows numeric format"
	default:
		return "invalid input syntax for type json"
	}
}
