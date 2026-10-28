package openflow

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// Schema coverage (LIC-3/a). The test walks every object schema that a flow
// document can reach from `OpenFlow` in the vendored openflow.openapi.yaml
// and requires each declared property to be decoded by the decoder's field
// tables or listed here as ignored, with a reason. The members of a oneOf
// are checked together, because one decoder serves them all. Decoded names
// the schema lacks must be listed as extra, with the finding that shows
// Windmill reads them. A schema change at a new pin therefore fails the
// test until the decoder or these lists follow it.

const (
	schemaFile   = "schema/openflow.openapi.yaml"
	schemaSHA256 = "6e1a84317bff02fefdd0bfee7092dbc46ddb0f1120ce5e039c9ebf8094d138e3"
	schemaPin    = "1.767.0"
)

type schemaMapping struct {
	decoded []string
	// ignored are schema properties the decoder leaves unread.
	ignored map[string]string
	// extra are decoded names that the schema does not declare.
	extra map[string]string
	// opaque are decoded properties kept verbatim or as a free map; the walk
	// does not descend into them.
	opaque map[string]string
}

func coverageMap() map[string]schemaMapping {
	tool := append([]string{"tool_type"}, fieldNames(mcpToolFields)...)
	return map[string]schemaMapping{
		"OpenFlow": {
			decoded: fieldNames(flowFields),
			extra: map[string]string{
				"timeout":                "flow metadata: the whole-flow timeout (OF-16)",
				"ws_error_handler_muted": "flow metadata that CE rejects when true (OF-2)",
			},
			opaque: map[string]string{"schema": "the input JSON Schema, kept verbatim; no execution semantics (OF-18)"},
		},
		"FlowValue": {
			decoded: fieldNames(flowValueFields),
			ignored: map[string]string{
				"notes":  "editor-only annotations (OF-4)",
				"groups": "editor-only annotations (OF-4)",
			},
			extra: map[string]string{
				"custom_debounce_key": "alias of debounce_key (OF-16)",
				"delete_after_use":    "decoded and dropped (OF-12)",
			},
			opaque: map[string]string{"flow_env": "values kept verbatim (OF-16)"},
		},
		"FlowModule": {
			decoded: fieldNames(moduleFields),
			extra: map[string]string{
				"delete_after_use":         "decoded and dropped (OF-12)",
				"apply_preprocessor":       "envelope field (OF-5)",
				"pass_flow_input_directly": "envelope field (OF-5, OF-V4)",
			},
		},
		"FlowModule.skip_if": {decoded: fieldNames(skipIfFields)},
		"FlowModule.mock": {
			decoded: fieldNames(mockFields),
			opaque:  map[string]string{"return_value": "any JSON, kept verbatim (OF-5)"},
		},
		"FlowModule.suspend": {
			decoded: fieldNames(suspendFields),
			opaque:  map[string]string{"resume_form": "any JSON, kept verbatim (OF-5)"},
		},
		"FlowModule.debouncing": {
			decoded: fieldNames(debouncingFields),
			extra:   map[string]string{"custom_debounce_key": "alias of debounce_key (OF-5)"},
		},
		"StopAfterIf":       {decoded: fieldNames(stopAfterIfFields)},
		"Retry":             {decoded: fieldNames(retryFields)},
		"Retry.constant":    {decoded: fieldNames(constantRetryFields)},
		"Retry.exponential": {decoded: fieldNames(exponentialRetryFields)},
		"RetryIf":           {decoded: fieldNames(retryIfFields)},
		"InputTransform":    {decoded: transformFieldNames},
		"FlowModuleValue": {
			decoded: fieldNames(valueFieldTable),
			ignored: map[string]string{"flow": "Identity's schema-only flag; the typed identity has no fields (OF-9)"},
			extra: map[string]string{
				"input_transform":          "alias of input_transforms (OF-9, OF-V1)",
				"pass_flow_input_directly": "decoded on script and flow values, unused by the flow executor (OF-9, OF-V4)",
			},
			opaque: map[string]string{
				"assets":           "not executed; kept verbatim",
				"input_transforms": "a map of input transforms by parameter name (OF-9); AiAgent's listed keys are conventions, not fields",
			},
		},
		"BranchOne.branches[]": {
			decoded: fieldNames(branchFields),
			extra: map[string]string{
				"skip_failure": "every branch decodes it (OF-6)",
				"parallel":     "every branch decodes it, unused by name (OF-6)",
			},
		},
		"BranchAll.branches[]": {
			decoded: fieldNames(branchFields),
			extra: map[string]string{
				"expr":     "every branch decodes it, default \"\" (OF-6)",
				"parallel": "every branch decodes it, unused by name (OF-6)",
			},
		},
		"AgentTool": {decoded: fieldNames(agentToolFields)},
		"ToolValue": {decoded: tool},
	}
}

func TestVendoredSchema(t *testing.T) {
	data, err := os.ReadFile(schemaFile)
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != schemaSHA256 {
		t.Fatalf("%s was modified: its SHA-256 no longer matches schema/NOTICE", schemaFile)
	}
	var doc struct {
		Info struct {
			Version string `yaml:"version"`
			License struct {
				Name string `yaml:"name"`
			} `yaml:"license"`
		} `yaml:"info"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Info.Version != schemaPin || doc.Info.License.Name != "Apache 2.0" {
		t.Fatalf("info: version %q, licence %q", doc.Info.Version, doc.Info.License.Name)
	}
	notice, err := os.ReadFile("schema/NOTICE")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(notice), schemaSHA256) {
		t.Fatal("schema/NOTICE does not record the schema's SHA-256")
	}
	if _, err := os.Stat("schema/LICENSE-APACHE"); err != nil {
		t.Fatal(err)
	}
}

// spec: LIC-3/a
func TestSchemaCoverage(t *testing.T) {
	data, err := os.ReadFile(schemaFile)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	mappings := coverageMap()
	w := &schemaWalk{
		t:        t,
		schemas:  doc.Components.Schemas,
		mappings: mappings,
		found:    map[string]map[string]bool{},
		visited:  map[string]bool{},
	}
	w.component("OpenFlow")

	// The schemas LIC-3/a names are all reached.
	for _, name := range []string{"FlowValue", "FlowModule", "RawScript", "PathScript", "PathFlow", "ForloopFlow", "WhileloopFlow", "BranchOne", "BranchAll", "Identity", "Retry", "StopAfterIf", "InputTransform"} {
		if !w.visited[name] {
			t.Errorf("schema %s is not reached from OpenFlow", name)
		}
	}

	for key, props := range w.found {
		m, ok := mappings[key]
		if !ok {
			t.Errorf("schema object %s (properties %v) has no mapping", key, sorted(props))
			continue
		}
		for p := range props {
			if !slices.Contains(m.decoded, p) && m.ignored[p] == "" {
				t.Errorf("%s.%s is neither decoded nor ignored", key, p)
			}
		}
		for p := range m.ignored {
			switch {
			case !props[p]:
				t.Errorf("%s: ignored %q is not a schema property", key, p)
			case slices.Contains(m.decoded, p):
				t.Errorf("%s: %q is both decoded and ignored", key, p)
			}
		}
		for _, p := range m.decoded {
			if !props[p] && m.extra[p] == "" {
				t.Errorf("%s: decoded %q is not in the schema and not listed as extra", key, p)
			}
		}
		for p := range m.extra {
			if props[p] || !slices.Contains(m.decoded, p) {
				t.Errorf("%s: extra %q must be decoded and absent from the schema", key, p)
			}
		}
		for p := range m.opaque {
			if !props[p] || !slices.Contains(m.decoded, p) {
				t.Errorf("%s: opaque %q must be a decoded schema property", key, p)
			}
		}
	}
	for key := range mappings {
		if w.found[key] == nil {
			t.Errorf("mapping %s matches no schema object", key)
		}
	}
	t.Logf("schema objects checked: %d", len(w.found))
}

type schemaWalk struct {
	t        *testing.T
	schemas  map[string]map[string]any
	mappings map[string]schemaMapping
	// found maps each schema object, by key, to its declared properties.
	found   map[string]map[string]bool
	visited map[string]bool
}

func (w *schemaWalk) component(name string) {
	if w.visited[name] {
		return
	}
	w.visited[name] = true
	s, ok := w.schemas[name]
	if !ok {
		w.t.Fatalf("unresolved $ref to %s", name)
	}
	w.object(name, name, s)
}

// object records the properties of s under key and descends into them.
// Nested inline objects are named after prefix.
func (w *schemaWalk) object(key, prefix string, s map[string]any) {
	if ref, ok := s["$ref"].(string); ok {
		w.component(refName(ref))
		return
	}
	for _, part := range maps(s["allOf"]) {
		if ref, ok := part["$ref"].(string); ok {
			w.component(refName(ref))
		} else {
			w.object(key, prefix, part)
		}
	}
	for _, part := range maps(s["oneOf"]) {
		if ref, ok := part["$ref"].(string); ok {
			// A oneOf member joins the union at key.
			name := refName(ref)
			w.visited[name] = true
			w.object(key, name, w.schemas[name])
		} else {
			w.object(key, prefix, part)
		}
	}
	props, _ := s["properties"].(map[string]any)
	if len(props) == 0 {
		return
	}
	if w.found[key] == nil {
		w.found[key] = map[string]bool{}
	}
	m := w.mappings[key]
	for p, ps := range props {
		w.found[key][p] = true
		if m.ignored[p] != "" || m.opaque[p] != "" {
			continue
		}
		if sub, ok := ps.(map[string]any); ok {
			w.property(prefix+"."+p, sub)
		}
	}
}

func (w *schemaWalk) property(path string, s map[string]any) {
	if ref, ok := s["$ref"].(string); ok {
		w.component(refName(ref))
		return
	}
	if items, ok := s["items"].(map[string]any); ok {
		w.property(path+"[]", items)
		return
	}
	if addl, ok := s["additionalProperties"].(map[string]any); ok {
		w.property(path+"{}", addl)
	}
	if s["properties"] != nil || s["allOf"] != nil || s["oneOf"] != nil {
		w.object(path, path, s)
	}
}

func refName(ref string) string {
	return strings.TrimPrefix(ref, "#/components/schemas/")
}

func maps(v any) []map[string]any {
	list, _ := v.([]any)
	var out []map[string]any
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func sorted(set map[string]bool) []string {
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
