package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/invakid404/windmill-lite/internal/bundle"
)

// These tests measure what warm step modules cost a host process (ADR
// 0013): a Bun host compiled with `bun build --compile`
// (testdata/memhost/host.ts) runs host mode on bundles built from sync
// folders the tests generate, under host_test.go's supervisor, and reports
// its resident set after a full garbage collection as distinct step modules
// load. Each folder is run in three layouts:
//
//   - per-script: each step's bundle inlines its own copy of the module
//     the steps share, as the build does today (ADR 0006's `warm`);
//   - shared-instance: that module is a host-provided module (D12), so the
//     host's one instance serves every step, as ADR 0013's options (a),
//     (b) and (c1) would;
//   - shared-code: the same folder built with shared code (ADR 0013): the
//     module is in its build group's closure, compiled
//     once per process, and each step module makes an instance of its own,
//     so each step keeps module state of its own.
//
// TestModuleMemory is the public synthetic reproduction and runs wherever
// the host-mode tests run. TestModuleMemoryClosure is the closure shaped
// like the embedder's, with npm packages; it needs the network once for
// its packages and about 2 GiB of memory, and runs only with
// WL_MEMORY_PROBE set.

// memLayouts are the three layouts, in the order they run.
var memLayouts = []string{"per-script", "shared-instance", "shared-code"}

// memSample is what the host reports at a point of a run.
type memSample struct {
	RSS     int64 `json:"rss"`
	Peak    int64 `json:"peak"`
	Heap    int64 `json:"heap"`
	Modules int   `json:"modules"`
}

func (m memSample) mib() float64 { return float64(m.RSS) / (1 << 20) }

// memRuns is the host's answer to /run: the runs and successes over all
// rounds, the first run that did not succeed, and the last round's
// outcomes.
type memRuns struct {
	MS           float64         `json:"ms"`
	Total        int             `json:"total"`
	Succeeded    int             `json:"succeeded"`
	FirstFailure json.RawMessage `json:"firstFailure"`
	Runs         []struct {
		Status string          `json:"status"`
		Result json.RawMessage `json:"result"`
	} `json:"runs"`
}

func memoryBun(t *testing.T) string {
	t.Helper()
	bun := os.Getenv("WL_BUN")
	if bun == "" {
		if os.Getenv("WL_REQUIRE_BUN") != "" {
			t.Fatal("WL_REQUIRE_BUN is set but WL_BUN is not")
		}
		t.Skip("WL_BUN is not set; it names the pinned Bun 1.3.10")
	}
	return bun
}

var (
	memHostOnce sync.Once
	memHostBin  string
	memHostErr  error
)

// memHost compiles testdata/memhost/host.ts once per test binary.
func memHost(t *testing.T, bun string) string {
	t.Helper()
	memHostOnce.Do(func() {
		dir, err := os.MkdirTemp("", "wl-memhost-")
		if err != nil {
			memHostErr = err
			return
		}
		memHostBin = filepath.Join(dir, "memhost")
		memHostErr = compileHost(bun, "testdata/memhost/host.ts", memHostBin)
	})
	if memHostErr != nil {
		t.Fatal(memHostErr)
	}
	return memHostBin
}

func compileHost(bun, entry, out string) error {
	cmd := exec.Command(bun, "build", "--compile", entry, "--outfile", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("bun build --compile %s: %v\n%s", entry, err, b)
	}
	return nil
}

// buildMemBundle builds the sync folder with its host contract into out,
// with shared code when shared is set.
func buildMemBundle(t *testing.T, bun, folder string, contract []byte, out string, shared bool) {
	t.Helper()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	res, err := bundle.Build(context.Background(), bundle.Config{
		Folder:         folder,
		Contract:       contract,
		EngineVersion:  resolveVersion(),
		Bun:            bun,
		Helper:         filepath.Join(repo, "harnesses", "bun", "build", "main.ts"),
		HarnessRuntime: filepath.Join(repo, "harnesses", "bun", "runtime"),
		Out:            out,
		Work:           out + ".work",
		SharedCode:     shared,
	})
	if err != nil {
		t.Fatalf("bundle.Build %s: %v", folder, err)
	}
	// Every script of the layout must have its shared layout: one that
	// keeps its per-script bundle would measure per-script memory.
	for _, w := range res.Warnings {
		if strings.HasPrefix(w, bundle.WarningSharedCodeFallback) {
			t.Errorf("bundle.Build %s: %s", folder, w)
		}
	}
}

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// memFlow is a flow of the given steps of f/<dir>/<prefix>NN, each passing
// its result to the next as prev.
func memFlow(summary, dir, format string, steps []int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "summary: %s\nvalue:\n  modules:\n", summary)
	for k, n := range steps {
		id := fmt.Sprintf(format, n)
		prev := ""
		if k > 0 {
			prev = ", prev: {type: javascript, expr: previous_result}"
		}
		fmt.Fprintf(&b, "    - id: %s\n      value: {type: script, path: f/%s/%s, input_transforms: {i: {type: javascript, expr: flow_input.i}%s}}\n", id, dir, id, prev)
	}
	return b.String()
}

// memHarness is one layout's host under the supervisor.
type memHarness struct {
	t *testing.T
	s *supervisor
}

func (h memHarness) sample() memSample {
	h.t.Helper()
	var m memSample
	h.s.getJSON(h.t, "/rss", &m)
	return m
}

func (h memHarness) run(path string, concurrent, reps int) memRuns {
	h.t.Helper()
	body, _ := json.Marshal(map[string]any{"path": path})
	resp, err := h.s.client.Post(fmt.Sprintf("http://host/run?concurrent=%d&reps=%d", concurrent, reps), "application/json", bytes.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		h.t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		h.t.Fatalf("run %s: the host answered %d: %s", path, resp.StatusCode, b)
	}
	var r memRuns
	if err := json.Unmarshal(b, &r); err != nil {
		h.t.Fatalf("run %s: %v: %s", path, err, b)
	}
	if want := concurrent * reps; r.Total != want || r.Succeeded != want || len(r.Runs) != concurrent {
		h.t.Fatalf("run %s: %d of %d runs succeeded, %d wanted, %d outcomes in the last round; first failure: %s", path, r.Succeeded, r.Total, want, len(r.Runs), r.FirstFailure)
	}
	return r
}

// synthStrings is the shared module of the synthetic reproduction: 40,000
// distinct strings of 102 characters, about 4.2 MB of source, and a
// counter of the calls its importers make.
func synthStrings() string {
	var b strings.Builder
	b.WriteString("// ADR 0013's synthetic reproduction: 40,000 distinct strings (generated).\nexport const state = { calls: 0 };\nexport const strings: string[] = [\n")
	r := rand.New(rand.NewPCG(13, 6))
	for i := range 40000 {
		fmt.Fprintf(&b, "  \"s%05d-", i)
		for range 6 {
			fmt.Fprintf(&b, "%016x", r.Uint64())
		}
		b.WriteString("\",\n")
	}
	b.WriteString("];\n")
	return b.String()
}

// TestModuleMemory runs ADR 0013's public synthetic reproduction: six Bun
// scripts that each import one generated module of 40,000 distinct
// strings, a flow of one of them and a flow of all six. Per script, each
// further module costs the host several MiB, the size of the shared
// module's source and the strings it makes; with one shared instance, or
// with the shared code of the prototype, it costs almost nothing. Each
// step also counts its calls in the shared module's state: per script and
// with shared code every step has a counter of its own, as on Windmill;
// with one shared instance they all count on one.
func TestModuleMemory(t *testing.T) {
	bun := memoryBun(t)
	host := memHost(t, bun)
	dir := t.TempDir()
	big := synthStrings()

	for _, layout := range memLayouts {
		t.Run(layout, func(t *testing.T) {
			folder := filepath.Join(dir, layout)
			files := map[string]string{
				"wmill.yaml":               "defaultTs: bun\nincludes:\n  - f/**\n",
				"f/mem/strings.ts":         big,
				"f/mem/one.flow/flow.yaml": memFlow("one step, one module", "mem", "s%d", []int{1}),
				"f/mem/six.flow/flow.yaml": memFlow("six steps, six modules", "mem", "s%d", []int{1, 2, 3, 4, 5, 6}),
			}
			imports := `import { state, strings } from "./strings.ts";`
			contract := "roots:\n  - flow: f/mem/one\n  - flow: f/mem/six\n"
			hostModules := map[string]string{}
			switch layout {
			case "shared-instance":
				contract += "host_modules: [f/mem/strings.ts]\n"
				hostModules = map[string]string{"f/mem/strings.ts": filepath.Join(folder, "f", "mem", "strings.ts")}
			}
			for n := 1; n <= 6; n++ {
				files[fmt.Sprintf("f/mem/s%d.ts", n)] = fmt.Sprintf("%s\n\nexport async function main(i: number = 0, prev: Record<string, number> = {}) {\n  state.calls++;\n  return { ...prev, s%d: state.calls, len: strings[(i + %d) %% strings.length].length };\n}\n", imports, n, n)
			}
			writeFiles(t, folder, files)
			out := filepath.Join(dir, layout+".bundle")
			buildMemBundle(t, bun, folder, []byte(contract), out, layout == "shared-code")
			mods, _ := json.Marshal(hostModules)
			h := memHarness{t, superviseHost(t, []string{host}, out, 0, "WL_HOST_MODULES="+string(mods))}

			connect := h.sample()
			h.run("f/mem/one", 1, 1)
			one := h.sample()
			six := h.run("f/mem/six", 1, 1)
			all := h.sample()
			h.run("f/mem/six", 8, 1)
			eight := h.sample()
			perModule := (all.mib() - one.mib()) / 5
			t.Logf("%s: %.1f MiB at connect, %.1f after one module, %.1f after six (%.2f MiB per further module), %.1f after eight concurrent runs",
				layout, connect.mib(), one.mib(), all.mib(), perModule, eight.mib())
			if one.Modules != 1 || all.Modules != 6 {
				t.Fatalf("modules loaded: %d after one step, %d after six; want 1 and 6", one.Modules, all.Modules)
			}

			// The counters the six steps returned: s1 ran once before, in
			// the one-step flow.
			var counts map[string]float64
			if err := json.Unmarshal(six.Runs[0].Result, &counts); err != nil {
				t.Fatal(err)
			}
			want := map[string]float64{"s1": 2, "s2": 1, "s3": 1, "s4": 1, "s5": 1, "s6": 1}
			if layout == "shared-instance" {
				want = map[string]float64{"s1": 2, "s2": 3, "s3": 4, "s4": 5, "s5": 6, "s6": 7}
			}
			for k, v := range want {
				if counts[k] != v {
					t.Errorf("%s: step %s counted %v calls; want %v (all: %v)", layout, k, counts[k], v, counts)
				}
			}

			// Per script, a further module costs at least the size of the
			// shared module's source (about 4.2 MB); shared, almost nothing.
			if layout == "per-script" && perModule < 3 {
				t.Errorf("per-script: %.2f MiB per further module; the reproduction expects at least 3", perModule)
			}
			if layout != "per-script" && perModule > 1.5 {
				t.Errorf("%s: %.2f MiB per further module; want at most 1.5", layout, perModule)
			}
		})
	}
}

// TestModuleMemoryClosure runs ADR 0013's closure shaped like the
// embedder's: 34 CommonJS codebase steps that each import one shared module
// outside the sync folder, which imports mongoose, the stock
// windmill-client, js-yaml and Babel's parser and types (pinned by
// testdata/memory/closure.bun.lock) and a generated package shaped like a
// generated API client, so that each step's bundle is about 6.9 MB, as the
// embedder's are on average. As published packages do, the shared module
// imports node:async_hooks by import() and the generated package imports
// node:fs/promises by import(), both kept in the closure: modules the
// runtime has built in, so every step keeps its shared layout. It reports
// the host's resident set with one module, with all 34, with eight
// concurrent runs of them, and after a soak of 1,000 runs, as the embedder
// soaked its host. With shared code, ADR 0013's memory gate holds: each
// further module costs at most 16 MiB and the soak's peak stays within the
// embedder's 650 MiB budget. It runs only with WL_MEMORY_PROBE set.
func TestModuleMemoryClosure(t *testing.T) {
	if os.Getenv("WL_MEMORY_PROBE") == "" {
		t.Skip("WL_MEMORY_PROBE is not set; this probe needs the network once and about 2 GiB of memory")
	}
	bun := memoryBun(t)
	const steps = 34
	dir := t.TempDir()
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	// The packages, then the generated one beside them.
	for src, dst := range map[string]string{"closure.package.json": "package.json", "closure.bun.lock": "bun.lock"} {
		b, err := os.ReadFile(filepath.Join("testdata", "memory", src))
		if err != nil {
			t.Fatal(err)
		}
		writeFiles(t, dir, map[string]string{dst: string(b)})
	}
	install := exec.Command(bun, "install", "--frozen-lockfile")
	install.Dir = dir
	if b, err := install.CombinedOutput(); err != nil {
		t.Fatalf("bun install: %v\n%s", err, b)
	}
	writeFiles(t, filepath.Join(dir, "node_modules", "synth-sdk"), synthSDK(90, 60))
	writeFiles(t, filepath.Join(dir, "lib"), map[string]string{"shared.ts": closureShared})

	all := make([]int, steps)
	for k := range all {
		all[k] = k + 1
	}
	for _, layout := range memLayouts {
		t.Run(layout, func(t *testing.T) {
			folder := filepath.Join(dir, layout)
			files := map[string]string{
				"wmill.yaml":                 "defaultTs: bun\nincludes:\n  - f/**\ncodebases:\n  - relative_path: ../lib\n    includes:\n      - f/cb/**\n",
				"f/flows/e1.flow/flow.yaml":  memFlow("one step", "cb", "s%02d", []int{1}),
				"f/flows/all.flow/flow.yaml": memFlow("every step", "cb", "s%02d", all),
			}
			imports := `import { client, describe, model, storage } from "../../../lib/shared";`
			contract := "roots:\n  - flow: f/flows/e1\n  - flow: f/flows/all\n"
			host := memHost(t, bun)
			hostModules := map[string]string{}
			switch layout {
			case "shared-instance":
				// The host imports the module statically, as a host's own
				// program does, since a compiled host resolves no package
				// at run time.
				contract += "host_modules: [../lib/shared.ts]\n"
				entry := filepath.Join(dir, "host-"+layout+".ts")
				writeFiles(t, dir, map[string]string{filepath.Base(entry): fmt.Sprintf(
					"import * as shared from %q;\n(globalThis as any)[Symbol.for(\"windmill-lite.memhost.modules\")] = { \"../lib/shared.ts\": shared };\nawait import(%q);\n",
					filepath.Join(dir, "lib", "shared.ts"), filepath.Join(repo, "cmd", "windmill-lite", "testdata", "memhost", "host.ts"))})
				host = filepath.Join(dir, "host-"+layout)
				if err := compileHost(bun, entry, host); err != nil {
					t.Fatal(err)
				}
			}
			for n := 1; n <= steps; n++ {
				files[fmt.Sprintf("f/cb/s%02d.ts", n)] = fmt.Sprintf("%s\n\nexport async function main(i: number = 0) {\n  const own = Array.from({ length: %d }, (_, k) => k * i);\n  const s = await storage();\n  return { step: %d, d: describe(i), m: model.modelName, calls: client.calls, own: own.length, store: s.run(%d, () => s.getStore()), read: typeof client.readFile };\n}\n", imports, n, n, n)
			}
			writeFiles(t, folder, files)
			out := filepath.Join(dir, layout+".bundle")
			buildMemBundle(t, bun, folder, []byte(contract), out, layout == "shared-code")
			mods, _ := json.Marshal(hostModules)
			h := memHarness{t, superviseHost(t, []string{host}, out, 0, "WL_HOST_MODULES="+string(mods))}

			connect := h.sample()
			h.run("f/flows/e1", 1, 1)
			one := h.sample()
			first := h.run("f/flows/all", 1, 1)
			loaded := h.sample()
			h.run("f/flows/all", 8, 1)
			eight := h.sample()
			soak := h.run("f/flows/all", 8, 125)
			after := h.sample()
			mib := func(b int64) float64 { return float64(b) / (1 << 20) }
			perModule := (loaded.mib() - one.mib()) / (steps - 1)
			t.Logf("%s: %.1f MiB at connect, %.1f after one module, %.1f after %d (%.2f MiB per further module; first run %.0f ms), %.1f after eight concurrent runs, %.1f after 1,000 runs, 125 rounds of eight (%.0f ms); peak %.1f MiB",
				layout, connect.mib(), one.mib(), loaded.mib(), steps, perModule, first.MS, eight.mib(), after.mib(), soak.MS, mib(after.Peak))
			if loaded.Modules != steps {
				t.Fatalf("modules loaded: %d; want %d", loaded.Modules, steps)
			}
			if layout == "shared-code" && perModule > 16 {
				t.Errorf("shared-code: %.2f MiB per further module; want at most 16", perModule)
			}
			if layout == "shared-code" && mib(after.Peak) > 650 {
				t.Errorf("shared-code: peak %.1f MiB after the soak; the embedder's budget is 650", mib(after.Peak))
			}
		})
	}
}

// closureShared is the shared module of the embedder-shaped closure.
const closureShared = `// ADR 0013's closure shaped like the embedder's (cmd/windmill-lite/memory_test.go).
import mongoose from "mongoose";
import * as wmill from "windmill-client";
import * as yaml from "js-yaml";
import { parse } from "@babel/parser";
import * as t from "@babel/types";
import * as sdk from "synth-sdk";

const Thing = new mongoose.Schema({ name: String, tags: [String], n: Number, at: Date });
export const model = mongoose.models.Thing ?? mongoose.model("Thing", Thing);
export const client = new sdk.Svc000({ key: "k" });
export function describe(x: unknown) {
  const ast = parse(` + "`const v = ${JSON.stringify(x)};`" + `);
  return { yaml: yaml.dump({ x }), client: typeof wmill.getVariable, schema: Object.keys(Thing.paths).length, decl: t.isVariableDeclaration(ast.program.body[0]), services: sdk.services.length };
}
export async function storage() {
  const { AsyncLocalStorage } = await import("node:async_hooks");
  return new AsyncLocalStorage<number>();
}
`

// synthSDK generates a CommonJS package shaped like a generated API client:
// modules services of a class with methods methods each, and a table of
// request schemas per module, all of which run when the package loads. Its
// base class reads files through node:fs/promises, which it imports by
// import() at its first read.
func synthSDK(modules, methods int) map[string]string {
	r := rand.New(rand.NewPCG(13, 8))
	word := func() string {
		b := make([]byte, 4+r.IntN(7))
		for i := range b {
			b[i] = byte('a' + r.IntN(26))
		}
		return string(b)
	}
	pick := func(s ...string) string { return s[r.IntN(len(s))] }
	files := map[string]string{
		"package.json": `{"name":"synth-sdk","version":"1.0.0","main":"index.js","license":"Unlicense"}` + "\n",
		"lib/base.js": `"use strict";
class Base {
  constructor(opts) { this.opts = Object.assign({ retries: 3, timeout: 30000 }, opts); this.calls = 0; }
  request(method, path, params, schema) { this.calls++; return { method, path, params, schema, opts: this.opts }; }
  async readFile(path) { const fs = await import("node:fs/promises"); return fs.readFile(path, "utf8"); }
}
exports.Base = Base;
`,
	}
	var index strings.Builder
	index.WriteString("\"use strict\";\nconst { Base } = require(\"./lib/base\");\n")
	for m := range modules {
		name := fmt.Sprintf("Svc%03d", m)
		var b strings.Builder
		b.WriteString("\"use strict\";\nconst { Base } = require(\"./base\");\nconst SCHEMAS = {\n")
		for k := range methods {
			var props []string
			for range 4 {
				props = append(props, fmt.Sprintf("%s: { type: %q, description: \"%s %s %s\" }", word(), pick("string", "number", "boolean", "object"), word(), word(), word()))
			}
			fmt.Fprintf(&b, "  m%d: { required: [%q, %q], properties: { %s } },\n", k, word(), word(), strings.Join(props, ", "))
		}
		fmt.Fprintf(&b, "};\nclass %s extends Base {\n", name)
		for k := range methods {
			fmt.Fprintf(&b, "  %s%d(params = {}) { if (params.%s === undefined && %d %% 3 === 0) params.%s = %q; return this.request(%q, \"/v1/%s/%s/{%s}\", params, SCHEMAS.m%d); }\n",
				word(), k, word(), k, word(), word(), pick("GET", "POST", "PUT", "DELETE", "PATCH"), word(), word(), word(), k)
		}
		fmt.Fprintf(&b, "}\nexports.%s = %s;\nexports.SCHEMAS = SCHEMAS;\n", name, name)
		files[fmt.Sprintf("lib/svc%03d.js", m)] = b.String()
		fmt.Fprintf(&index, "exports.%s = require(\"./lib/svc%03d\").%s;\n", name, m, name)
	}
	index.WriteString("exports.Base = Base;\nexports.services = Object.keys(exports).filter((k) => k.startsWith(\"Svc\"));\n")
	files["index.js"] = index.String()
	return files
}
