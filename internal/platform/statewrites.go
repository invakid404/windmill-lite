package platform

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// The resource writes a step makes through the job API to keep its state
// ([WA-13]): createResource, updateResourceValue and deleteResource, which
// the stock client's setState makes, for resources of type state. The
// services check the request as Windmill does and change their
// ResourceStore; the facade answers with their texts.
//
// [WA-13]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-13

// StateType is the resource type that windmill-lite writes: state, the type
// setState creates ([WA-13]). The other types keep the facade's 501.
//
// [WA-13]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-13
const StateType = "state"

// ErrProvidedResource is the refusal of an update or a delete of a resource
// that a provider holds: the host's or the bundle's resources, which are
// configuration, possibly chosen per run (H13). windmill-lite does not
// write them (ADR 0001, section 5a), and the facade answers 501.
var ErrProvidedResource = errors.New("the resource is provided by the host or the bundle, which windmill-lite does not write")

// ErrNotStateResource is the refusal of an update or a delete of a stored
// entry whose type is not state: windmill-lite writes only state, and the
// facade answers it as it answers the writes of other types, 501.
var ErrNotStateResource = errors.New("the stored resource is not of type state")

// storeFailure is a failure of the resource store, which the API answers
// 500 `Internal: windmill-lite: the resource store failed: <err>`, so that
// a getState in an outage throws rather than reading a state never written.
type storeFailure struct{ err error }

func (e storeFailure) Error() string {
	return "windmill-lite: the resource store failed: " + e.err.Error()
}
func (e storeFailure) Unwrap() error { return e.err }

// readError is the API's error for a failed read of a resource: 500, with
// the failure's text, marked as a store failure when the store failed.
func readError(err error) *Error {
	var sf storeFailure
	return &Error{Variant: "InternalErr", Message: err.Error(), StoreFailure: errors.As(err, &sf)}
}

// storeWorkspace is the workspace the store keys the services' entries
// under: Workspace, or the default host's.
func (s *Services) storeWorkspace() string {
	if s.Workspace != "" {
		return s.Workspace
	}
	return DefaultHost.Workspace
}

// provided reads the resource at path from the providers alone.
func (s *Services) provided(ctx context.Context, path string) (bool, error) {
	if s == nil || s.Resources == nil {
		return false, nil
	}
	_, ok, err := s.Resources.Resource(ctx, path)
	return ok, err
}

// invalidPath is the text of Windmill's refusal of a create at a path that
// is not of a resource's form ([WA-13]/j).
//
// [WA-13]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/conformance.md#wa-13
const invalidPath = "Invalid path: it must be of the form u/<user>/<name>, f/<folder>/<name> or g/<group>/<name>, where every segment contains only alphanumeric characters, '_' or '-'"

// validResourcePath reports whether a create may use path: u, f or g, then
// at least two segments, every segment not empty and made of letters,
// ASCII digits, '_' and '-'. It is the narrowest rule that the recorded
// cases allow ([WA-13]/j): a dot, a space, an empty segment and another
// prefix are refused, and a non-ASCII letter (ü) is accepted, so letters of
// any script are; a digit outside ASCII, which no case records, is refused.
//
// [WA-13]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/conformance.md#wa-13
func validResourcePath(path string) bool {
	segs := strings.Split(path, "/")
	if len(segs) < 3 {
		return false
	}
	switch segs[0] {
	case "u", "f", "g":
	default:
		return false
	}
	for _, seg := range segs {
		if seg == "" {
			return false
		}
		for _, r := range seg {
			if !unicode.IsLetter(r) && (r < '0' || r > '9') && r != '_' && r != '-' {
				return false
			}
		}
	}
	return true
}

// storedValue is the value a write stores: JSON null when the request has
// no value ([WA-13]/e, /f), else the request's value with its numbers
// spelled as the route stores them (respellNumbers) in jsonb's stored form.
// A value that jsonb refuses (a NUL, a lone surrogate, a number beyond
// numeric) is answered 400 `SqlErr: error returned from database: <reason>`
// ([WA-13]/h), without the source location Windmill appends (D14).
//
// [WA-13]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/conformance.md#wa-13
func storedValue(value json.RawMessage, create bool) (json.RawMessage, *Error) {
	if value == nil {
		return json.RawMessage("null"), nil
	}
	stored, err := jsonval.FromProducer(respellNumbers(value, create)).Stored()
	if err != nil {
		var se *jsonval.StoreError
		if errors.As(err, &se) {
			return nil, &Error{Variant: "SqlErr", Message: "error returned from database: " + se.DatabaseMessage()}
		}
		return nil, &Error{Variant: "SqlErr", Message: "error returned from database: " + err.Error()}
	}
	return stored, nil
}

// CreateStateResource answers createResource for a resource of type state
// at path ([WA-13]/d, /f, /j): the path's form is checked first (400
// `Bad request: Invalid path: …`), then the value (storedValue); a path
// that a provider or the store holds answers 400 `Bad request: Resource
// <path> already exists` and changes nothing. value is nil when the
// request has none, which stores null.
//
// [WA-13]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/conformance.md#wa-13
func (s *Services) CreateStateResource(ctx context.Context, path string, value json.RawMessage) error {
	if !validResourcePath(path) {
		return &Error{Variant: "BadRequest", Message: invalidPath}
	}
	v, e := storedValue(value, true)
	if e != nil {
		return e
	}
	exists := &Error{Variant: "BadRequest", Message: "Resource " + path + " already exists"}
	switch held, err := s.provided(ctx, path); {
	case err != nil:
		return internal("%v", err)
	case held:
		return exists
	}
	ok, err := s.ResourceStore.Create(ctx, s.storeWorkspace(), path, Resource{Type: StateType, Value: v})
	switch {
	case err != nil:
		return readError(storeFailure{err})
	case !ok:
		return exists
	}
	return nil
}

// UpdateStateResource answers updateResourceValue ([WA-13]/e): the value of
// the stored state at path becomes value (null when nil), its type kept. A
// provider's resource is refused with ErrProvidedResource, a stored entry
// of another type with ErrNotStateResource; no entry answers 404
// `Not found: Resource <path> not found` and creates nothing.
//
// The type is read before the update, in two operations of the store, so
// an entry of another type that a host's own writer puts at the path in
// between is updated; windmill-lite itself creates only state.
//
// [WA-13]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/conformance.md#wa-13
func (s *Services) UpdateStateResource(ctx context.Context, path string, value json.RawMessage) error {
	missing := notFound("Resource %s not found", path)
	if err := s.writableState(ctx, path, missing); err != nil {
		return err
	}
	v, e := storedValue(value, false)
	if e != nil {
		return e
	}
	ok, err := s.ResourceStore.UpdateValue(ctx, s.storeWorkspace(), path, v)
	switch {
	case err != nil:
		return readError(storeFailure{err})
	case !ok:
		return missing
	}
	return nil
}

// DeleteStateResource answers deleteResource ([WA-13]/f): the stored state
// at path is removed. As UpdateStateResource, a provider's resource and an
// entry of another type are refused; no entry answers 404 `Not found:
// Resource not found at name <path>`.
//
// [WA-13]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/conformance.md#wa-13
func (s *Services) DeleteStateResource(ctx context.Context, path string) error {
	missing := notFound("Resource not found at name %s", path)
	if err := s.writableState(ctx, path, missing); err != nil {
		return err
	}
	ok, err := s.ResourceStore.Delete(ctx, s.storeWorkspace(), path)
	switch {
	case err != nil:
		return readError(storeFailure{err})
	case !ok:
		return missing
	}
	return nil
}

// writableState checks that path names a stored entry of type state that no
// provider shadows: missing when there is no entry.
func (s *Services) writableState(ctx context.Context, path string, missing *Error) error {
	switch held, err := s.provided(ctx, path); {
	case err != nil:
		return internal("%v", err)
	case held:
		return ErrProvidedResource
	}
	r, ok, err := s.ResourceStore.Get(ctx, s.storeWorkspace(), path)
	switch {
	case err != nil:
		return readError(storeFailure{err})
	case !ok:
		return missing
	case r.Type != StateType:
		return ErrNotStateResource
	}
	return nil
}

// respellNumbers rewrites the numbers of a JSON text as a write route
// stores them ([WA-13]/i), strings and the rest of the text unchanged. One
// rule for each route spells every number the recordings hold:
//
//   - update_value (create false) stores a number as the backend's JSON
//     library reads it: an integer without fraction or exponent that fits
//     64 bits as it is, and every other number, -0 included, as a float
//     printed with its shortest digits and, when it is whole and has at
//     most 16 integer digits, a fraction of ".0" (1e3 → 1000.0, -0 → 0.0,
//     1.50 → 1.5, 1E-2 → 0.01).
//   - create (create true) does the same, but a number written without a
//     decimal point whose value is whole is stored as that integer (1e3 →
//     1000, -0 → 0), while 1.0 stays 1.0.
//
// jsonb's stored form (jsonval) then writes the result as PostgreSQL's
// numeric does. The rules are fitted to the recorded spellings; a number
// that no case records follows them.
//
// [WA-13]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/conformance.md#wa-13
func respellNumbers(src []byte, create bool) []byte {
	out := make([]byte, 0, len(src))
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(src) && src[j] != '"' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(src))
			out = append(out, src[i:j]...)
			i = j
		case c == '-' || c >= '0' && c <= '9':
			j := i + 1
			for j < len(src) && strings.IndexByte("0123456789.eE+-", src[j]) >= 0 {
				j++
			}
			out = append(out, respellNumber(string(src[i:j]), create)...)
			i = j
		default:
			out = append(out, c)
			i++
		}
	}
	return out
}

// respellNumber is one number of respellNumbers.
func respellNumber(t string, create bool) string {
	hasFrac := strings.ContainsRune(t, '.')
	mant, exp, hasExp := strings.Cut(strings.ToLower(t), "e")
	switch {
	case !hasFrac && !hasExp:
		if t == "-0" {
			if create {
				return "0"
			}
			return floatText(t)
		}
		if _, err := strconv.ParseInt(t, 10, 64); err == nil {
			return t
		}
		if _, err := strconv.ParseUint(t, 10, 64); err == nil {
			return t
		}
		if create {
			// Whole, beyond 64 bits: the integer as written.
			return t
		}
		return floatText(t)
	case create && !hasFrac:
		if whole, ok := wholeText(mant, exp); ok {
			return whole
		}
	}
	return floatText(t)
}

// wholeText is the integer that the integer mantissa mant times ten to the
// power exp is, and false when it is not whole.
func wholeText(mant, exp string) (string, bool) {
	e, err := strconv.Atoi(exp)
	if err != nil {
		return "", false
	}
	neg := strings.HasPrefix(mant, "-")
	digits := strings.TrimLeft(strings.TrimPrefix(mant, "-"), "0")
	if digits == "" {
		return "0", true
	}
	switch {
	case e >= 0:
		if e > 1<<16 {
			return "", false
		}
		digits += strings.Repeat("0", e)
	case len(digits) > -e && strings.Trim(digits[len(digits)+e:], "0") == "":
		digits = digits[:len(digits)+e]
	default:
		return "", false
	}
	if neg {
		return "-" + digits, true
	}
	return digits, true
}

// floatText is t read as a float64 and printed as the backend's JSON
// library prints one, before jsonb's numeric spells it: the shortest
// digits that read back as the same float, in plain decimal, with ".0"
// after a whole value of at most 16 integer digits; zero is 0.0, since
// numeric has no negative zero. A number beyond float64's range is left as
// written, and jsonb refuses it.
func floatText(t string) string {
	f, err := strconv.ParseFloat(t, 64)
	if err != nil || math.IsInf(f, 0) {
		return t
	}
	if f == 0 {
		return "0.0"
	}
	neg := f < 0
	sci := strconv.FormatFloat(math.Abs(f), 'e', -1, 64)
	m, e, _ := strings.Cut(sci, "e")
	digits := strings.Replace(m, ".", "", 1)
	exp, _ := strconv.Atoi(e)
	k := exp + 1 // the position of the decimal point after the first digit
	var s string
	switch {
	case k <= 0:
		s = "0." + strings.Repeat("0", -k) + digits
	case k >= len(digits):
		s = digits + strings.Repeat("0", k-len(digits))
		if k <= 16 {
			s += ".0"
		}
	default:
		s = digits[:k] + "." + digits[k:]
	}
	if neg {
		return "-" + s
	}
	return s
}

// WriteBodySyntaxError checks the body of a resource write as
// JSONSyntaxError does, except inside the top-level member "value": the
// body parser keeps that member as raw JSON text, so a lone surrogate
// escape there is not its error but the database's, which storedValue
// gives (WA-13/h).
func WriteBodySyntaxError(text []byte) error {
	p := &parser{src: text, rawMember: "value"}
	if _, err := p.value(); err != nil {
		return err
	}
	p.space()
	if p.pos < len(p.src) {
		return p.fail("trailing characters", p.pos+1)
	}
	return nil
}
