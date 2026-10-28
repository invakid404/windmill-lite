package bundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/invakid404/windmill-lite/internal/engine"
	"github.com/invakid404/windmill-lite/internal/ir"
)

// FormatVersion is the version of the bundle format this package writes
// and reads.
const FormatVersion = 1

// ManifestFile is the manifest's path in a bundle.
const ManifestFile = "manifest.json"

// Manifest is a bundle's manifest.json (ADR 0010). It lists the digest of
// every other file of the bundle and of every source file the build read,
// so the bundle digest, the SHA-256 of its canonical encoding, changes
// with any of them.
type Manifest struct {
	// Format is FormatVersion.
	Format int    `json:"format"`
	Engine Engine `json:"engine"`
	// Bundlers are the versions of the bundlers that built the scripts:
	// Bun always, esbuild once codebase scripts are built (C2b).
	Bundlers Bundlers `json:"bundlers"`
	// NonconformantRuntime is set when the Bun that built the bundle is not
	// the pin (`--bun-mismatch`, ADR 0010).
	NonconformantRuntime bool `json:"nonconformant_runtime"`
	// ModuleState is the module-state mode the bundle runs in (ADR 0006).
	ModuleState engine.ModuleState `json:"module_state"`
	// Deviations are the named deviations of ADR 0001 in effect for the
	// bundle, sorted by number: those its content puts in effect, those of
	// its module-state mode, and those every bundle has. Deviations that
	// depend on a run-time setting are the run trace's.
	Deviations []string `json:"deviations"`
	// Contract is the host contract the bundle was compiled against: with
	// no contract given, every flow and script of the folder is a root.
	Contract ir.Contract `json:"contract"`
	// HostModules are the host-provided modules (D12) in registration
	// order: a module after the modules it imports.
	HostModules []HostModuleEntry `json:"host_modules"`
	// HarnessRuntimes holds, by harness class, the digest of the harness
	// runtime the bundle was built for (TreeDigest of its directory).
	HarnessRuntimes map[string]string `json:"harness_runtimes"`
	// Sources holds the digest of every file of the sync folder the build
	// read, by its path from the sync root: wmill.yaml, flows, scripts,
	// their metadata and locks, inline script files, the workspace scripts
	// that bundles inline, and the data files.
	Sources map[string]string `json:"sources"`
	// Files holds the digest of every other file of the bundle, by its
	// path in the bundle.
	Files map[string]string `json:"files"`
}

// Engine describes the engine that built the bundle.
type Engine struct {
	// Version is the engine's exact version. A runtime opens a bundle only
	// if it is its own (ADR 0010).
	Version string `json:"version"`
	// ExpressionStackBytes is the expression engine's stack limit, which is
	// observable as the depth at which recursion overflows (ADR 0003,
	// JE-48).
	ExpressionStackBytes int `json:"expression_stack_bytes"`
}

// Bundlers are the bundler versions of a build.
type Bundlers struct {
	Bun     string `json:"bun"`
	Esbuild string `json:"esbuild,omitempty"`
}

// HostModuleEntry is one host-provided module: its key in the host
// contract and its ID, which names its bundled copy,
// scripts/bun/provided/<ID>.mjs.
type HostModuleEntry struct {
	Key string `json:"key"`
	ID  string `json:"id"`
}

// Canonical returns the manifest's canonical encoding, the bytes of
// manifest.json.
func (m *Manifest) Canonical() ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return Canonicalize(b)
}

// Canonicalize returns the canonical encoding of a JSON text: object keys
// sorted by their UTF-8 bytes, numbers as written, strings escaped only
// where JSON requires it (and U+2028, U+2029), two-space indentation, and
// a final newline. Of a repeated object key, the last value is kept, so a
// text that repeats a key is never canonical.
func Canonicalize(text []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(text))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("bundle: JSON text has data after its value")
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Digest is the digest the manifest records for content: "sha256:" and
// its hexadecimal SHA-256.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ParseManifest reads a manifest.json: it must be the canonical encoding
// of a manifest of this format, with no unknown fields. It returns the
// manifest and the bundle digest, the digest of data.
func ParseManifest(data []byte) (*Manifest, string, error) {
	canon, err := Canonicalize(data)
	if err != nil {
		return nil, "", fmt.Errorf("bundle: %s: %w", ManifestFile, err)
	}
	if !bytes.Equal(canon, data) {
		return nil, "", fmt.Errorf("bundle: %s is not in canonical form", ManifestFile)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, "", fmt.Errorf("bundle: %s: %w", ManifestFile, err)
	}
	if m.Format != FormatVersion {
		return nil, "", fmt.Errorf("bundle: %s: format %d, this engine reads format %d", ManifestFile, m.Format, FormatVersion)
	}
	for p := range m.Files {
		if p == ManifestFile || !validID(p) {
			return nil, "", fmt.Errorf("bundle: %s lists the file %q", ManifestFile, p)
		}
	}
	for _, h := range m.HostModules {
		if _, ok := m.Files["scripts/bun/provided/"+h.ID+".mjs"]; !ok || !validID(h.ID) {
			return nil, "", fmt.Errorf("bundle: %s lists the host-provided module %q (%s) without its bundled copy", ManifestFile, h.Key, h.ID)
		}
	}
	return &m, Digest(data), nil
}

// manifestDeviations are the deviations a bundle lists: the program's,
// those of its module-state mode, and those of every bundle.
func manifestDeviations(prog []string, mode engine.ModuleState) []string {
	set := map[string]bool{}
	for _, d := range prog {
		set[d] = true
	}
	// Every bundle: windmill-lite job ids (D3), the interrupted expression
	// attempt (D8), no Rust location suffix in generated messages (D14).
	for _, d := range []string{"D3", "D8", "D14"} {
		set[d] = true
	}
	// warm mode; fresh removes them (ADR 0006).
	if mode == engine.ModuleStateWarm {
		for _, d := range []string{"D1", "D2", "D9", "D10", "D11"} {
			set[d] = true
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	slices.SortFunc(out, func(a, b string) int { return deviationNumber(a) - deviationNumber(b) })
	return out
}

func deviationNumber(d string) int {
	n := 0
	fmt.Sscanf(d, "D%d", &n)
	return n
}
