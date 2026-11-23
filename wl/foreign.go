package wl

import (
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// foreignOrigins checks Options.ForeignWindmillOrigins and returns them as
// the welcomes carry them: each as scheme://host[:port], in lower case,
// without the scheme's default port, and each once, in their first order,
// so "https://Windmill.example.com:443/" names the origin that
// "https://windmill.example.com" does.
//
// The harness reads each origin with the URL parser that reads a request's
// URL, the WHATWG URL Standard's, and stops on one it cannot read. So this
// check accepts only what that parser accepts and reads the same way
// (foreignOrigin), and refuses the rest here, where the host learns why. An
// error names the value's position and the reason, never the value, which
// can hold a secret in its user information or its query.
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
// accepts an http or https URL with nothing after its host and port but an
// optional "/", whose host is one of:
//
//   - a DNS name: labels of ASCII letters, digits, hyphens and underscores,
//     none empty, the last not a number, which the URL parser would read as
//     an IPv4 address, and none starting with "xn--", whose Punycode the URL
//     parser checks by rules that this check does not have;
//   - an IPv4 address in four decimal parts, without leading zeros;
//   - an IPv6 address in brackets, without a zone.
//
// So an internationalised name is refused, in Unicode and in its xn-- form.
func foreignOrigin(v string) (string, string) {
	switch {
	case strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r > '~' }):
		return "", "it holds a space, a control character or a character outside ASCII"
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
	case u.Opaque != "" || u.Host == "":
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

// foreignHost returns the host h, as url.URL.Hostname gives it, in the
// welcome's form, or why foreignOrigin refuses it; bracketed says that the
// URL wrote it in brackets.
func foreignHost(h string, bracketed bool) (string, string) {
	if bracketed {
		a, err := netip.ParseAddr(h)
		if err != nil || !a.Is6() || a.Zone() != "" {
			return "", "its host is not an IPv6 address without a zone"
		}
		return "[" + a.String() + "]", ""
	}
	h = strings.ToLower(h)
	labels := strings.Split(h, ".")
	if last := labels[len(labels)-1]; last != "" && (strings.Trim(last, "0123456789") == "" || strings.HasPrefix(last, "0x") && strings.Trim(last[2:], "0123456789abcdef") == "") {
		// The URL parser reads a host that ends in a number as an IPv4
		// address, and refuses one that is not a valid address.
		a, err := netip.ParseAddr(h)
		if err != nil || !a.Is4() {
			return "", "its host ends in a number and is not an IPv4 address in four decimal parts"
		}
		return a.String(), ""
	}
	for _, l := range labels {
		switch {
		case l == "":
			return "", "its host has an empty label"
		case strings.Trim(l, "abcdefghijklmnopqrstuvwxyz0123456789-_") != "":
			return "", "its host has a character other than an ASCII letter, a digit, a hyphen or an underscore"
		case strings.HasPrefix(l, "xn--"):
			return "", "its host has a label that starts with xn--"
		}
	}
	return h, ""
}
