package facade

import (
	"encoding/hex"
	"fmt"
	"mime"
	"strings"
	"unicode/utf8"

	"github.com/invakid404/windmill-lite/internal/platform"
)

// The rejections of Windmill's web framework, which refuses a request
// whose path, query or JSON body it cannot extract before any handler
// runs ([WA-42]): their bodies carry no error variant's prefix, and they
// are typed text/plain; charset=utf-8, the framework's type for a text
// answer. The handlers that take such parameters call these first, after
// the token check that precedes them on Windmill too ([WA-4]).
//
// [WA-42]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-42
// [WA-4]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-4

// textUTF8 is the content type of a handler's successful text answer and
// of the framework's rejections ([WA-42], Content types); an error
// variant's answer is bare text/plain (text).
//
// [WA-42]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-42
const textUTF8 = "text/plain; charset=utf-8"

// plainText is a text answer typed as a handler's successful string
// answer and the framework's rejections are: text/plain; charset=utf-8.
func plainText(status int, body string) Reply {
	return Reply{Status: status, Headers: map[string]string{"content-type": textUTF8}, Body: []byte(body)}
}

// pathUUID reads the path parameter of c that Windmill's route takes as a
// UUID, named name in the route's template, and returns its canonical
// form (lower case, hyphenated), the form windmill-lite's job ids take.
// A value that is not a UUID is refused as the framework refuses it, with
// a 400 whose body is "Invalid URL: Cannot parse `<wire>` with value
// `<value>`: UUID parsing failed: <detail>": wire is the parameter's name
// in Windmill's own route, which can differ from the API schema's (job_id
// for id in the flow user state routes, [WA-14]).
//
// [WA-14]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-14
func pathUUID(c *Call, name, wire string) (string, *Reply) {
	v := c.Params[name]
	id, detail, ok := parseUUID(v)
	if !ok {
		r := plainText(400, fmt.Sprintf("Invalid URL: Cannot parse `%s` with value `%s`: UUID parsing failed: %s", wire, v, detail))
		return "", &r
	}
	return id, nil
}

// queryUUID reads the optional query parameter name, which Windmill's
// route takes as a UUID: "" when the query does not carry it, else its
// canonical form. A value that is not a UUID, the empty one included, is
// refused with a 400, as the framework refuses a query string it cannot
// deserialise; the reference gives the status only, so the text is
// windmill-lite's.
func queryUUID(c *Call, name string) (string, *Reply) {
	if !c.Query.Has(name) {
		return "", nil
	}
	v := c.Query.Get(name)
	id, detail, ok := parseUUID(v)
	if !ok {
		r := plainText(400, fmt.Sprintf("windmill-lite: the query parameter %s, %s, is not a UUID: %s", name, jsonString(v), detail))
		return "", &r
	}
	return id, nil
}

// jsonBody checks the body of a route that takes a JSON body, as the
// framework does before the handler: without a JSON content type
// (application/json or application/<anything>+json, parameters allowed)
// it answers 415 "Expected request with `Content-Type: application/json`",
// and for a body that does not parse, an empty one included, 400 "Failed
// to parse the request body as JSON: <message>", the message in the
// backend JSON library's words.
func jsonBody(c *Call) *Reply {
	mt, _, err := mime.ParseMediaType(c.Headers["content-type"])
	typ, sub, _ := strings.Cut(mt, "/")
	if err != nil || typ != "application" || sub != "json" && !strings.HasSuffix(sub, "+json") {
		r := plainText(415, "Expected request with `Content-Type: application/json`")
		return &r
	}
	if err := platform.JSONSyntaxError(c.Body); err != nil {
		r := plainText(400, "Failed to parse the request body as JSON: "+err.Error())
		return &r
	}
	return nil
}

// uuidGroups are the lengths, in hex digits, of a hyphenated UUID's five
// groups.
var uuidGroups = [5]int{8, 4, 4, 4, 12}

// parseUUID parses s as the UUID parser of Windmill's backend does: 32
// hex digits; the hyphenated form, 8-4-4-4-12; or the hyphenated form in
// braces ({…}) or after urn:uuid:; hex digits in either case. It returns
// the canonical form, or the parser's reason why s is not one, in the
// words [WA-42] gives:
//
//   - "invalid character: found `<c>` at <i>" for the first character
//     that is neither a hex digit nor a hyphen, at its 1-based byte
//     position in s;
//   - "invalid length: found <n>", n bytes, when there is no hyphen, no
//     braces and no urn:uuid: prefix;
//   - "invalid group count: expected 5, found <n>" for a hyphenated form
//     with other than five groups;
//   - "invalid group length in group <g>: expected <e>, found <n>" for the
//     first group, counted from 0, whose length is wrong.
//
// The reference does not say from where the character's position and the
// group are counted; these bases are windmill-lite's reading.
//
// [WA-42]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/client-api.md#wa-42
func parseUUID(s string) (id, detail string, ok bool) {
	inner, offset, simple := s, 0, true
	switch {
	case len(s) >= 2 && s[0] == '{' && s[len(s)-1] == '}':
		inner, offset, simple = s[1:len(s)-1], 1, false
	case strings.HasPrefix(s, "urn:uuid:"):
		inner, offset, simple = s[len("urn:uuid:"):], len("urn:uuid:"), false
	}
	var hyphens []int
	for i, r := range inner {
		switch {
		case r == '-':
			hyphens = append(hyphens, i)
		case r >= utf8.RuneSelf || !isHex(byte(r)):
			return "", fmt.Sprintf("invalid character: found `%c` at %d", r, offset+i+1), false
		}
	}
	if len(hyphens) == 0 && simple {
		if len(inner) == 32 {
			return canonicalUUID(inner), "", true
		}
		return "", fmt.Sprintf("invalid length: found %d", len(s)), false
	}
	if len(hyphens) != 4 {
		return "", fmt.Sprintf("invalid group count: expected 5, found %d", len(hyphens)+1), false
	}
	start := 0
	for g, want := range uuidGroups {
		end := len(inner)
		if g < 4 {
			end = hyphens[g]
		}
		if end-start != want {
			return "", fmt.Sprintf("invalid group length in group %d: expected %d, found %d", g, want, end-start), false
		}
		start = end + 1
	}
	return canonicalUUID(strings.ReplaceAll(inner, "-", "")), "", true
}

// canonicalUUID writes 32 hex digits as a lower-case hyphenated UUID.
func canonicalUUID(digits string) string {
	b, _ := hex.DecodeString(digits)
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func isHex(b byte) bool {
	return '0' <= b && b <= '9' || 'a' <= b && b <= 'f' || 'A' <= b && b <= 'F'
}
