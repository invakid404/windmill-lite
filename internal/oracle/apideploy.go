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
// generate-metadata writes for the same source, and, for a fixture with
// oracle.pull, the value the CLI's sync pull writes back (SY-6, CF-6). It
// is provenance, never compared (OR-V5).
type FlowDeployment struct {
	Method       string          `json:"method"`
	Submitted    json.RawMessage `json:"submitted"`
	Materialized json.RawMessage `json:"materialized"`
	CLIGenerated json.RawMessage `json:"cli_generated"`
	Pulled       json.RawMessage `json:"pulled,omitempty"`
}

// scriptMeta is the part of a <path>.script.yaml that an API deployment
// reads; any other key is refused.
type scriptMeta struct {
	Summary     string `json:"summary"`
	Description string `json:"description"`
	CacheTTL    *int   `json:"cache_ttl"`
	// Envs, Schema and DeleteAfterSecs are sent as written: a script's
	// envs (WR-22), the stored schema validation builds from (WR-54), and
	// delete_after_secs (OR-21), which the CLI's metadata cannot give.
	Envs            []string        `json:"envs"`
	Schema          json.RawMessage `json:"schema"`
	DeleteAfterSecs *int            `json:"delete_after_secs"`
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
	s.Envs, s.Schema, s.DeleteAfterSecs = m.Envs, m.Schema, m.DeleteAfterSecs
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
	if err := resolveFlowValue(doc["value"], flowDir, hashes); err != nil {
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

// resolveFlowValue resolves the modules of a flow value: those of its
// module list and its failure and preprocessor modules, and, through
// containers, those of every loop body and branch (OpenFlow's FlowValue,
// FlowModule and the container values). Only a module's own value is
// touched: a rawscript's "!inline <file>" content and lock are read from
// the flow's directory, and a by-path script's PinToDeployedHash becomes
// the hash its path was deployed under. Everything else is data the server
// receives as written (FX-7): input transforms and static values, an
// iterator, a mock, a stop or skip expression, even when such a value is an
// object that looks like a module value.
func resolveFlowValue(v any, flowDir string, hashes map[string]string) error {
	fv, ok := v.(map[string]any)
	if !ok {
		return errors.New("the value is not an object")
	}
	if err := resolveModuleList(fv["modules"], flowDir, hashes); err != nil {
		return err
	}
	for _, k := range []string{"failure_module", "preprocessor_module"} {
		if m, ok := fv[k]; ok && m != nil {
			if err := resolveModule(m, flowDir, hashes); err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
		}
	}
	return nil
}

func resolveModuleList(v any, flowDir string, hashes map[string]string) error {
	if v == nil {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return errors.New("a module list is not a list")
	}
	for _, m := range list {
		if err := resolveModule(m, flowDir, hashes); err != nil {
			return err
		}
	}
	return nil
}

func resolveModule(v any, flowDir string, hashes map[string]string) error {
	m, ok := v.(map[string]any)
	if !ok {
		return errors.New("a module is not an object")
	}
	id, _ := m["id"].(string)
	val, ok := m["value"].(map[string]any)
	if !ok {
		return fmt.Errorf("module %q has no value object", id)
	}
	switch val["type"] {
	case "rawscript":
		for _, k := range []string{"content", "lock"} {
			s, ok := val[k].(string)
			if !ok || !strings.HasPrefix(s, "!inline ") {
				continue
			}
			name := strings.TrimPrefix(s, "!inline ")
			if filepath.IsAbs(name) || strings.Contains(filepath.ToSlash(name), "/") {
				return fmt.Errorf("module %q: %s %q is not a file of the flow's directory", id, k, name)
			}
			b, err := os.ReadFile(filepath.Join(flowDir, name))
			if err != nil {
				return fmt.Errorf("module %q: %w", id, err)
			}
			val[k] = string(b)
		}
	case "script":
		if val["hash"] == PinToDeployedHash {
			p, _ := val["path"].(string)
			h, ok := hashes[p]
			if !ok {
				return fmt.Errorf("module %q pins script %s, which the tree does not deploy", id, p)
			}
			val["hash"] = h
		}
	case "forloopflow", "whileloopflow":
		return resolveModuleList(val["modules"], flowDir, hashes)
	case "branchone":
		if err := resolveBranches(val["branches"], flowDir, hashes); err != nil {
			return err
		}
		return resolveModuleList(val["default"], flowDir, hashes)
	case "branchall":
		return resolveBranches(val["branches"], flowDir, hashes)
	}
	return nil
}

func resolveBranches(v any, flowDir string, hashes map[string]string) error {
	if v == nil {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return errors.New("branches is not a list")
	}
	for _, b := range list {
		br, ok := b.(map[string]any)
		if !ok {
			return errors.New("a branch is not an object")
		}
		if err := resolveModuleList(br["modules"], flowDir, hashes); err != nil {
			return err
		}
	}
	return nil
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
