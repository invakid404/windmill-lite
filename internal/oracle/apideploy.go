package oracle

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/invakid404/windmill-lite/internal/yamlx"
)

// PinToDeployedHash is the script hash that an API-deployed flow uses to
// pin a by-path script module to the version the deployment created: the
// harness replaces it with the hash Windmill assigned that path. A tree
// cannot carry the real hash, which exists only once the server has
// deployed the script (OF-14).
const PinToDeployedHash = "0000000000000000"

// APIDeployment is what DeployAPI created.
type APIDeployment struct {
	// Folders are the folder names created.
	Folders []string
	// Scripts maps each script path to the hash Windmill assigned it.
	Scripts map[string]string
	// Flows maps each flow path to the value submitted for it, as JSON.
	Flows map[string]json.RawMessage
}

// FlowDeployment is a recording's evidence of how an API-deployed fixture's
// target flow reached the oracle: the value submitted, the value the API
// answers once its dependency job has succeeded, and the value the CLI's
// generate-metadata writes for the same source. It is provenance, never
// compared (OR-V5).
type FlowDeployment struct {
	Method       string          `json:"method"`
	Submitted    json.RawMessage `json:"submitted"`
	Materialized json.RawMessage `json:"materialized"`
	CLIGenerated json.RawMessage `json:"cli_generated"`
}

// scriptMeta is the part of a <path>.script.yaml that an API deployment
// reads; any other key is refused.
type scriptMeta struct {
	Summary     string `json:"summary"`
	Description string `json:"description"`
	CacheTTL    *int   `json:"cache_ttl"`
}

// DeployAPI creates the folders, scripts and flows of the sync tree dir
// through the REST API, exactly as the tree writes them: nothing is
// regenerated or filled in, so a field the tree leaves out reaches the
// server absent. It reads a narrow tree:
//
//   - wmill.yaml, and folder.meta.yaml files, which are not sent;
//   - Bun scripts f/<folder>/<name>.ts, each with an optional
//     <name>.script.yaml giving summary, description and cache_ttl;
//   - flows f/<folder>/<name>.flow/flow.yaml, whose rawscript content and
//     lock may be "!inline <file>" in the flow's directory, as the CLI
//     writes them, and whose by-path script modules may pin
//     PinToDeployedHash.
//
// Anything else is refused. Folders are created first, then scripts, then
// flows, each in path order.
func DeployAPI(ctx context.Context, c *Client, workspace, dir string) (*APIDeployment, error) {
	d := &APIDeployment{Scripts: map[string]string{}, Flows: map[string]json.RawMessage{}}
	folders := map[string]bool{}
	var scripts, flows []string
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		parts := strings.Split(rel, "/")
		if parts[0] != "f" && rel != "wmill.yaml" {
			return fmt.Errorf("oracle: api deploy: %s is not under f/", rel)
		}
		if len(parts) >= 2 && parts[0] == "f" {
			folders[parts[1]] = true
		}
		switch {
		case e.IsDir() && strings.HasSuffix(e.Name(), ".flow"):
			if len(parts) != 3 {
				return fmt.Errorf("oracle: api deploy: flow %s is not f/<folder>/<name>.flow", rel)
			}
			flows = append(flows, strings.TrimSuffix(rel, ".flow"))
			return filepath.SkipDir
		case e.IsDir():
			if len(parts) > 2 {
				return fmt.Errorf("oracle: api deploy: directory %s is neither a folder nor a flow", rel)
			}
			return nil
		case rel == "wmill.yaml", e.Name() == "folder.meta.yaml", strings.HasSuffix(e.Name(), ".script.yaml"):
			return nil
		case strings.HasSuffix(e.Name(), ".ts") && strings.Count(e.Name(), ".") == 1 && len(parts) == 3:
			scripts = append(scripts, strings.TrimSuffix(rel, ".ts"))
			return nil
		}
		return fmt.Errorf("oracle: api deploy: %s is not a Bun script, a flow or metadata", rel)
	})
	if err != nil {
		return nil, err
	}
	for name := range folders {
		d.Folders = append(d.Folders, name)
	}
	sort.Strings(d.Folders)
	sort.Strings(scripts)
	sort.Strings(flows)
	for _, name := range d.Folders {
		if err := c.CreateFolder(ctx, workspace, name); err != nil {
			return nil, err
		}
	}
	for _, p := range scripts {
		s, err := readScript(dir, p)
		if err != nil {
			return nil, err
		}
		hash, err := c.CreateScript(ctx, workspace, s)
		if err != nil {
			return nil, err
		}
		if hash == "" {
			return nil, fmt.Errorf("oracle: api deploy: creating script %s answered no hash", p)
		}
		d.Scripts[p] = hash
	}
	for _, p := range flows {
		body, value, err := readFlow(dir, p, d.Scripts)
		if err != nil {
			return nil, err
		}
		if err := c.CreateFlow(ctx, workspace, body); err != nil {
			return nil, err
		}
		d.Flows[p] = value
	}
	return d, nil
}

func readScript(dir, path string) (NewScript, error) {
	s := NewScript{Path: path, Language: "bun"}
	content, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)+".ts"))
	if err != nil {
		return s, err
	}
	s.Content = string(content)
	metaPath := filepath.Join(dir, filepath.FromSlash(path)+".script.yaml")
	data, err := os.ReadFile(metaPath)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	j, err := yamlx.ToJSON(data)
	if err != nil {
		return s, fmt.Errorf("%s: %w", metaPath, err)
	}
	var m scriptMeta
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return s, fmt.Errorf("oracle: api deploy: %s: %w", metaPath, err)
	}
	s.Summary, s.Description, s.CacheTTL = m.Summary, m.Description, m.CacheTTL
	return s, nil
}

// readFlow returns the create body of the flow at path (its flow.yaml with
// path added, inline files read and pinned hashes resolved) and the value
// it submits.
func readFlow(dir, path string, hashes map[string]string) (json.RawMessage, json.RawMessage, error) {
	flowDir := filepath.Join(dir, filepath.FromSlash(path)+".flow")
	data, err := os.ReadFile(filepath.Join(flowDir, "flow.yaml"))
	if err != nil {
		return nil, nil, err
	}
	j, err := yamlx.ToJSON(data)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", flowDir, err)
	}
	var doc map[string]any
	dec := json.NewDecoder(bytes.NewReader(j))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", flowDir, err)
	}
	if doc == nil || doc["value"] == nil {
		return nil, nil, fmt.Errorf("oracle: api deploy: %s/flow.yaml has no value", flowDir)
	}
	if err := resolveModules(doc["value"], flowDir, hashes); err != nil {
		return nil, nil, fmt.Errorf("oracle: api deploy: flow %s: %w", path, err)
	}
	doc["path"] = path
	body, err := json.Marshal(doc)
	if err != nil {
		return nil, nil, err
	}
	value, err := json.Marshal(doc["value"])
	return body, value, err
}

// resolveModules reads a rawscript's "!inline <file>" content and lock from
// the flow's directory, and replaces a by-path script's PinToDeployedHash
// with the hash its path was deployed under. Other values are left as they
// are, so a static input that happens to read "!inline x" stays a string.
func resolveModules(v any, flowDir string, hashes map[string]string) error {
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if err := resolveModules(e, flowDir, hashes); err != nil {
				return err
			}
		}
	case map[string]any:
		switch x["type"] {
		case "rawscript":
			for _, k := range []string{"content", "lock"} {
				s, ok := x[k].(string)
				if !ok || !strings.HasPrefix(s, "!inline ") {
					continue
				}
				name := strings.TrimPrefix(s, "!inline ")
				if filepath.IsAbs(name) || strings.Contains(filepath.ToSlash(name), "/") {
					return fmt.Errorf("%s %q is not a file of the flow's directory", k, name)
				}
				b, err := os.ReadFile(filepath.Join(flowDir, name))
				if err != nil {
					return err
				}
				x[k] = string(b)
			}
		case "script":
			if x["hash"] == PinToDeployedHash {
				p, _ := x["path"].(string)
				h, ok := hashes[p]
				if !ok {
					return fmt.Errorf("script module pins %s, which the tree does not deploy", p)
				}
				x["hash"] = h
			}
		}
		for _, k := range sortedKeys(x) {
			if err := resolveModules(x[k], flowDir, hashes); err != nil {
				return err
			}
		}
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

// flowValue returns the value member of a flow as the API or a flow.yaml
// holds it, compacted.
func flowValue(flow json.RawMessage) (json.RawMessage, error) {
	var f struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(flow, &f); err != nil {
		return nil, err
	}
	if f.Value == nil {
		return nil, errors.New("oracle: the flow has no value")
	}
	var b bytes.Buffer
	if err := json.Compact(&b, f.Value); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
