package bundle

import (
	"strings"
	"testing"
)

// The Bun.env rewrite that Bun prints first in every built script, as the
// pinned Bun prints it (abridged): windmill-lite's own module, which the
// search leaves out.
const envSection = `// @bun
// wl/env-__windmill_lite_Bun.mjs
if (Reflect.getOwnPropertyDescriptor(globalThis, "__windmill_lite_Bun") === void 0) {
  let real = Reflect.get(globalThis, "Bun");
  Reflect.defineProperty(globalThis, "__windmill_lite_Bun", { value: real });
}

`

// TestArgsFileFree holds the search to its conservative rule: code with
// no way to a file, a process or code made at run time is cleared, and
// any capability, a property read by a built name or the file's name keeps
// the file, wherever it is written, strings and comments included, and
// however the generated sections are arranged.
func TestArgsFileFree(t *testing.T) {
	entry := func(body string) string {
		return envSection + "// entry/f/x/s.ts\n" + body + "\nexport {\n  main\n};\n"
	}
	for _, c := range []struct {
		name, code string
		free       bool
	}{
		{"echo", entry("async function main(p) {\n  return p;\n}"), true},
		{"arithmetic, env, arrays, maps", entry("var m = new Map;\nasync function main(x, a) {\n  let items = [];\n  items.push(x + 1, a[0]);\n  return { items, job: process.env.WM_JOB_ID, n: JSON.stringify(m.get(x) ?? null) };\n}"), true},
		{"no generated section", "// entry/f/x/s.ts\nasync function main(p) {\n  return p;\n}\nexport { main };\n", true},
		{"the file's name", entry(`async function main() {
  return "args.json";
}`), false},
		{"a built name through Bun.file", entry(`async function main() {
  return (await __windmill_lite_Bun.file(["args", "json"].join("."))).json();
}`), false},
		{"fetch of a file: URL", entry("async function main(d) {\n  return (await fetch(\"file://\" + d + \"/args\" + \".json\")).json();\n}"), false},
		{"dynamic import", entry("async function main(d) {\n  return import(d);\n}"), false},
		{"require", entry("async function main() {\n  return require(\"f\" + \"s\");\n}"), false},
		{"another process", entry("async function main() {\n  return process.binding(\"spawn_sync\");\n}"), false},
		{"computed member", entry("async function main(o, k) {\n  return o[k];\n}"), false},
		{"computed constructor", entry("async function main() {\n  return (0)[\"con\" + \"structor\"];\n}"), false},
		{"destructured computed key", entry("async function main(k) {\n  const { [k]: f } = 0;\n  return f;\n}"), false},
		{"the global object", entry("async function main() {\n  return globalThis;\n}"), false},
		{"this", entry("function main() {\n  return this;\n}"), false},
		{"reflection", entry("async function main(o, k) {\n  return Reflect.get(o, k);\n}"), false},
		{"eval", entry("async function main(s) {\n  return (0, eval)(s);\n}"), false},
		{"a capability in a string", entry("async function main() {\n  return \"Bun\";\n}"), false},
		{"a generated header after the script's code", "// @bun\n// entry/f/x/s.ts\nasync function main() {\n  return 1;\n}\n// wl/env-x.mjs\nBun.file(\"x\");\n", false},
		{"a header spelled inside a template literal", entry("const t = `\n// wl/evil\n`;\nasync function main() {\n  return __windmill_lite_Bun;\n}"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := argsFileFree([]byte(c.code)); got != c.free {
				t.Fatalf("argsFileFree = %t, want %t, for:\n%s", got, c.free, c.code)
			}
		})
	}
	// The generated section alone holds capabilities: it is left out only
	// when it comes first.
	if !argsFileFree([]byte(entry("function main() {\n  return 1;\n}"))) || argsFileFree([]byte(strings.Replace(entry("function main() {\n  return 1;\n}"), "// wl/env-", "// lw/env-", 1))) {
		t.Fatal("the generated section is not left out exactly when it is windmill-lite's and first")
	}
}
