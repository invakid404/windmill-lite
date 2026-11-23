package wl

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// foreignOrigins checks Options.ForeignWindmillOrigins and returns them as
// the welcomes carry them: each as scheme://host[:port], with the scheme
// and the host in lower case and without the scheme's default port, and
// each once, in their first order. A request URL's origin has this form
// too, so "https://Windmill.example.com:443/" names the origin that
// "https://windmill.example.com" does. The harness reads each with the URL
// parser that reads the request's URL, so the two compare equal.
func foreignOrigins(list []string) ([]string, error) {
	var out []string
	for _, v := range list {
		u, err := url.Parse(v)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil ||
			u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
			return nil, fmt.Errorf("wl: ForeignWindmillOrigins: %q is not an http or https origin, such as https://windmill.example.com", v)
		}
		host := strings.ToLower(u.Hostname())
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		if p := u.Port(); p != "" {
			// The URL parser of the harness refuses a port above 65535,
			// and reads "0443" as 443.
			n, err := strconv.Atoi(p)
			if err != nil || n > 65535 {
				return nil, fmt.Errorf("wl: ForeignWindmillOrigins: %q has a port outside 0 to 65535", v)
			}
			if !(u.Scheme == "http" && n == 80) && !(u.Scheme == "https" && n == 443) {
				host += ":" + strconv.Itoa(n)
			}
		}
		if o := u.Scheme + "://" + host; !slices.Contains(out, o) {
			out = append(out, o)
		}
	}
	return out, nil
}
