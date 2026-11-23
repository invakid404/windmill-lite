package wl

import (
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// foreignOrigins checks Options.ForeignWindmillOrigins and returns them as
// the welcomes carry them: each as scheme://host[:port], with the ASCII
// letters in lower case, without the scheme's default port, and each once,
// in their first order, so "https://Windmill.example.com:443/" names the
// origin that "https://windmill.example.com" does.
//
// The harness compares origins as the WHATWG URL Standard's parser gives
// them, the parser that reads a request's URL. This check refuses what that
// parser refuses or reads as something else, wherever it can decide it
// without the Unicode tables of internationalised names (foreignOrigin), so
// the host learns why here. A host outside ASCII reaches the harness whole,
// since the parser maps it before it reads it; the harness leaves out one
// that the parser then rejects: no request URL can have such a host. An error names the value's position and the reason, never
// the value, which can hold a secret in its user information or query.
func foreignOrigins(list []string) ([]string, error) {
	var out []string
	for i, v := range list {
		o, reason := foreignOrigin(v)
		if reason != "" {
			return nil, fmt.Errorf("wl: ForeignWindmillOrigins[%d] is not an http or https origin such as https://windmill.example.com: %s", i, reason)
		}
		if !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	return out, nil
}

// foreignOrigin returns v in the welcome's form, or why it is refused. It
// refuses a value that is not valid UTF-8, that holds a space, a control
// character, "@", "?", "#", "%" or a backslash anywhere, whose scheme is not
// http or https, that has a path other than "/" or a port above 65535, or
// whose host foreignHost refuses.
func foreignOrigin(v string) (string, string) {
	switch {
	case !utf8.ValidString(v):
		return "", "it is not valid UTF-8"
	case strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r >= 0x7f && r <= 0x9f }):
		return "", "it holds a space or a control character"
	case strings.Contains(v, "@"):
		return "", "it has user information"
	case strings.Contains(v, "?"):
		return "", "it has a query"
	case strings.Contains(v, "#"):
		return "", "it has a fragment"
	case strings.ContainsAny(v, `%\`):
		return "", "it holds a percent sign or a backslash"
	}
	u, err := url.Parse(v)
	switch {
	case err != nil:
		return "", "it is not a URL"
	case u.Scheme != "http" && u.Scheme != "https":
		return "", "its scheme is not http or https"
	case u.Opaque != "" || u.Hostname() == "":
		return "", "it has no host"
	case u.Path != "" && u.Path != "/":
		return "", "it has a path"
	}
	host, reason := foreignHost(u.Hostname(), strings.HasPrefix(u.Host, "["))
	if reason != "" {
		return "", reason
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n > 65535 {
			return "", "its port is above 65535"
		}
		// The URL parser reads "0443" as 443, and drops the default port.
		if !(u.Scheme == "http" && n == 80) && !(u.Scheme == "https" && n == 443) {
			host += ":" + strconv.Itoa(n)
		}
	}
	return u.Scheme + "://" + host, ""
}

// forbiddenHost are the ASCII characters that the URL parser refuses in a
// host name, beside the ones that foreignOrigin refuses everywhere.
const forbiddenHost = "/:<>[]^|\x7f"

// foreignHost returns the host h, as url.URL.Hostname gives it, in the
// welcome's form, or why foreignOrigin refuses it; bracketed says that the
// URL wrote it in brackets. As the URL parser does, it reads:
//
//   - a bracketed host as an IPv6 address, which may have no zone;
//   - a host with a character outside ASCII as a name, with its ASCII
//     letters in lower case and every other character kept: the harness's
//     parser maps it first, and only then reads it, so bücher.example
//     becomes xn--bcher-kva.example and １２７.0.0.1, in fullwidth digits,
//     the IPv4 address 127.0.0.1;
//   - an ASCII host whose last label is a number (digits, or "0x" and hex
//     digits) as an IPv4 address, in up to four parts of decimal, octal (a
//     leading "0") or hex numbers (ipv4), refusing one that is not a valid
//     address and giving the address's dotted form;
//   - any other host as a name, with its letters in lower case.
func foreignHost(h string, bracketed bool) (string, string) {
	if bracketed {
		a, err := netip.ParseAddr(h)
		if err != nil || !a.Is6() || a.Zone() != "" {
			return "", "its host is not an IPv6 address without a zone"
		}
		return "[" + a.String() + "]", ""
	}
	h = asciiLower(h)
	if strings.ContainsAny(h, forbiddenHost) {
		return "", "its host has a character that a host name cannot have"
	}
	if strings.ContainsFunc(h, func(r rune) bool { return r > '~' }) {
		return h, ""
	}
	if endsInNumber(h) {
		a, ok := ipv4(h)
		if !ok {
			return "", "its host ends in a number and is not an IPv4 address"
		}
		return a.String(), ""
	}
	return h, ""
}

// asciiLower returns s with its ASCII letters in lower case, and every other
// character as it is.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if 'A' <= r && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
}

// hostLabels returns the dot-separated labels of h, without one empty last
// label (a trailing dot) when there are others.
func hostLabels(h string) []string {
	labels := strings.Split(h, ".")
	if len(labels) > 1 && labels[len(labels)-1] == "" {
		labels = labels[:len(labels)-1]
	}
	return labels
}

// endsInNumber says whether the URL parser reads h as an IPv4 address: its
// last label is all digits, or an IPv4 number (ipv4Number).
func endsInNumber(h string) bool {
	labels := hostLabels(h)
	last := labels[len(labels)-1]
	if last != "" && strings.Trim(last, "0123456789") == "" {
		return true
	}
	_, ok := ipv4Number(last)
	return ok
}

// ipv4 parses h as the URL parser parses an IPv4 address: up to four
// numbers, each but the last at most 255, the last filling the remaining
// bytes.
func ipv4(h string) (netip.Addr, bool) {
	labels := hostLabels(h)
	if len(labels) > 4 {
		return netip.Addr{}, false
	}
	nums := make([]uint64, len(labels))
	for i, l := range labels {
		n, ok := ipv4Number(l)
		if !ok || i < len(labels)-1 && n > 255 {
			return netip.Addr{}, false
		}
		nums[i] = n
	}
	v := nums[len(nums)-1]
	if v >= 1<<(8*(5-len(nums))) {
		return netip.Addr{}, false
	}
	for i, n := range nums[:len(nums)-1] {
		v += n << (8 * (3 - i))
	}
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}), true
}

// ipv4Number parses one part of an IPv4 address as the URL parser does:
// "0x" and hex digits, "0" and octal digits, or decimal digits; "0x" alone
// is 0. A value of 2^40 or more stays 2^40 or more, which no address takes.
func ipv4Number(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	base := uint64(10)
	switch {
	case strings.HasPrefix(s, "0x"):
		s, base = s[2:], 16
	case len(s) > 1 && s[0] == '0':
		s, base = s[1:], 8
	}
	var n uint64
	for _, c := range s {
		d := uint64(strings.IndexRune("0123456789abcdef", c))
		if d >= base {
			return 0, false
		}
		if n < 1<<40 {
			n = n*base + d
		}
	}
	return n, true
}
