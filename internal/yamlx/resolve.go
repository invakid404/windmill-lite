package yamlx

import (
	"math"
	"math/big"
	"regexp"
	"strconv"
)

// The YAML 1.2 core schema (YAML 1.2.2, section 10.3.2), as the CLI's
// parser applies it. Only plain scalars are resolved; anything else is a
// string.
var (
	reNull     = regexp.MustCompile(`^(?:~|null|Null|NULL|)$`)
	reBool     = regexp.MustCompile(`^(?:true|True|TRUE|false|False|FALSE)$`)
	reDecimal  = regexp.MustCompile(`^[-+]?[0-9]+$`)
	reOctal    = regexp.MustCompile(`^0o[0-7]+$`)
	reHex      = regexp.MustCompile(`^0x[0-9a-fA-F]+$`)
	reFloat    = regexp.MustCompile(`^[-+]?(?:\.[0-9]+|[0-9]+(?:\.[0-9]*)?)(?:[eE][-+]?[0-9]+)?$`)
	reInfinity = regexp.MustCompile(`^[-+]?\.(?:inf|Inf|INF)$`)
	reNaN      = regexp.MustCompile(`^\.(?:nan|NaN|NAN)$`)
)

// resolvePlain resolves an untagged plain scalar.
func resolvePlain(out *Node, v string) {
	switch {
	case reNull.MatchString(v):
		out.Kind = Null
	case reBool.MatchString(v):
		setBool(out, v)
	case isInt(v):
		setInt(out, v)
	case isFloat(v):
		setFloat(out, v)
	default:
		out.Kind, out.Text = String, v
	}
}

// resolveTagged resolves a scalar with an explicit tag, named as cliTagName
// names it. A core tag whose form does not match the text, and any tag the
// CLI does not know, leave the text as a string: the CLI warns and keeps
// the string.
func resolveTagged(out *Node, tag, v string) {
	out.Kind, out.Text = String, v
	switch tag {
	case "!inline", "!inline_fileset":
		out.Text = tag + " " + v
	case coreTag + "null":
		if reNull.MatchString(v) {
			out.Kind, out.Text = Null, ""
		}
	case coreTag + "bool":
		if reBool.MatchString(v) {
			setBool(out, v)
		}
	case coreTag + "int":
		if isInt(v) {
			setInt(out, v)
		}
	case coreTag + "float":
		// The float forms exclude the integer forms: !!float 1 is "1".
		if isFloat(v) && !reDecimal.MatchString(v) {
			setFloat(out, v)
		}
	}
}

func isInt(v string) bool {
	return reDecimal.MatchString(v) || reOctal.MatchString(v) || reHex.MatchString(v)
}

func isFloat(v string) bool {
	return reFloat.MatchString(v) || reInfinity.MatchString(v) || reNaN.MatchString(v)
}

func setBool(out *Node, v string) {
	out.Kind = Bool
	if v[0] == 't' || v[0] == 'T' {
		out.Text = "true"
	} else {
		out.Text = "false"
	}
}

// setInt sets an integer form's value: the double nearest to the exact
// integer, as JavaScript's parseInt computes it.
func setInt(out *Node, v string) {
	out.Kind, out.Text = Number, v
	switch {
	case reOctal.MatchString(v):
		out.num = nearest(v[2:], 8)
	case reHex.MatchString(v):
		out.num = nearest(v[2:], 16)
	default:
		out.num = parseDecimal(v)
	}
}

func setFloat(out *Node, v string) {
	out.Kind, out.Text = Number, v
	switch {
	case reInfinity.MatchString(v):
		out.num = math.Inf(1)
		if v[0] == '-' {
			out.num = math.Inf(-1)
		}
	case reNaN.MatchString(v):
		out.num = math.NaN()
	default:
		out.num = parseDecimal(v)
	}
}

// parseDecimal converts a decimal integer or float form to the nearest
// double. Out-of-range values become an infinity or a zero, as in JavaScript.
func parseDecimal(v string) float64 {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil && !isRangeError(err) {
		// The forms are checked before conversion, so this cannot happen.
		panic("yamlx: unparsable number " + strconv.Quote(v))
	}
	return f
}

func isRangeError(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// nearest converts unsigned digits in base 8 or 16 to the double nearest
// to their exact value.
func nearest(digits string, base int) float64 {
	i, ok := new(big.Int).SetString(digits, base)
	if !ok {
		panic("yamlx: unparsable digits " + strconv.Quote(digits))
	}
	f, _ := new(big.Float).SetInt(i).Float64()
	return f
}
