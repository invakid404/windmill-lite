package conformance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"

	"go.yaml.in/yaml/v3"
)

// jsonNumber is the JSON number grammar. A YAML number that does not match
// it (0x1F, 1_000, .inf) is refused rather than reinterpreted, so a fixture
// value is always the JSON text its author wrote.
var jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// YAMLToJSON converts one YAML document to JSON text, keeping mapping order
// and number spelling. It is for windmill-lite's own fixture files
// (fixture.yaml, setup.yaml, runs/*.yaml), which are strict on purpose: a
// value is the JSON its author wrote. Aliases, merge keys, repeated keys
// and non-JSON numbers are refused. A sync tree's files are Windmill's and
// are read in the CLI's dialect (SY-9) by internal/yamlx instead.
func YAMLToJSON(data []byte) (json.RawMessage, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Kind == 0 {
		return json.RawMessage("null"), nil
	}
	var b bytes.Buffer
	if err := writeYAMLNode(&b, &doc); err != nil {
		return nil, err
	}
	return json.RawMessage(b.Bytes()), nil
}

func writeYAMLNode(b *bytes.Buffer, n *yaml.Node) error {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			b.WriteString("null")
			return nil
		}
		return writeYAMLNode(b, n.Content[0])
	case yaml.MappingNode:
		b.WriteByte('{')
		seen := make(map[string]bool, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Tag == "!!merge" {
				return fmt.Errorf("line %d: merge keys are not supported", k.Line)
			}
			if k.Kind != yaml.ScalarNode || (k.Tag != "!!str" && k.Tag != "!!int" && k.Tag != "!!float" && k.Tag != "!!bool") {
				return fmt.Errorf("line %d: mapping key must be a scalar", k.Line)
			}
			if seen[k.Value] {
				return fmt.Errorf("line %d: key %q given twice", k.Line, k.Value)
			}
			seen[k.Value] = true
			if i > 0 {
				b.WriteByte(',')
			}
			writeJSONString(b, k.Value)
			b.WriteByte(':')
			if err := writeYAMLNode(b, v); err != nil {
				return err
			}
		}
		b.WriteByte('}')
		return nil
	case yaml.SequenceNode:
		b.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeYAMLNode(b, c); err != nil {
				return err
			}
		}
		b.WriteByte(']')
		return nil
	case yaml.AliasNode:
		return fmt.Errorf("line %d: aliases are not supported", n.Line)
	case yaml.ScalarNode:
		switch n.Tag {
		case "!!null":
			b.WriteString("null")
		case "!!bool":
			var v bool
			if err := n.Decode(&v); err != nil {
				return fmt.Errorf("line %d: %v", n.Line, err)
			}
			if v {
				b.WriteString("true")
			} else {
				b.WriteString("false")
			}
		case "!!int", "!!float":
			if !jsonNumber.MatchString(n.Value) {
				return fmt.Errorf("line %d: number %q is not a JSON number", n.Line, n.Value)
			}
			b.WriteString(n.Value)
		default:
			// !!str, and any other tag (a sync tree's !inline values are
			// quoted strings): the scalar's text.
			writeJSONString(b, n.Value)
		}
		return nil
	}
	return fmt.Errorf("line %d: unsupported YAML node", n.Line)
}

func writeJSONString(b *bytes.Buffer, s string) {
	enc := json.NewEncoder(b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	b.Truncate(b.Len() - 1)
}
