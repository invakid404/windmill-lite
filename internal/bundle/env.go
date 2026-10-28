package bundle

import "fmt"

// Format is a bundle's module format.
type Format string

// The module formats: Bun.build always writes ESM; esbuild writes either
// (C2b, WR-47).
const (
	ESM Format = "esm"
	CJS Format = "cjs"
)

// EnvBinding is the global the define table points the global `Bun` at, and
// the support module defines. A bundler replaces only free references
// with a define, and a script cannot capture a name it never spells, so
// the build uses EnvBinding only when no input of a bundle contains it,
// and otherwise EnvBinding with a numeric suffix (the Bun helper picks it;
// see harnesses/bun/build).
const EnvBinding = "__windmill_lite_Bun"

// EnvRewrite returns the env rewrite of ADR 0010 step 5 for one output
// format: the define table both bundlers apply, and the source of the
// support module that every bundle imports before its own code.
//
// A harness gives each invocation its own view of process.env (WA-31), but
// Bun.env is a separate, read-only object that the runtime cannot replace.
// So the build points every read of the environment through Bun at
// process.env, through EnvBinding, a proxy of the real Bun whose `env` is
// process.env:
//
//   - `Bun.env` (also as `Bun["env"]`) becomes `EnvBinding.env`;
//   - `import.meta.env` becomes `EnvBinding.env` in ESM output only. In
//     CommonJS output import.meta stays empty, as in the CLI's esbuild
//     bundles, so code reading import.meta.env fails there as on Windmill;
//   - every other reference to the global `Bun`, also as `globalThis.Bun`,
//     `global.Bun` or `self.Bun` (so an alias such as `const B = Bun` and
//     destructuring such as `const { env } = Bun`), becomes EnvBinding.
//
// The support module defines EnvBinding on the global object, once per
// process. It is a module of its own, so the bundler links its names
// (globalThis, Reflect, Proxy, Object, process) to the real globals and
// renames a script's own top-level bindings of those names, as it renames
// any collision between modules; nothing is inserted into a script's scope.
// In ESM output the bundle imports it first; in CJS output (esbuild, C2b)
// it is injected the same way, so its source runs as either format.
//
// What a define cannot see still reads the real environment: imports of
// the "bun" module (`import { env } from "bun"`), optional chaining through
// the global object (`globalThis?.Bun`), reflection (`Reflect.get(globalThis,
// "Bun")`), an alias of the global object (`const g = globalThis; g.Bun`)
// and code built at run time (`eval`, `new Function`).
//
// The Bun build applies the rewrite to Bun-target bundles only: `//native`
// and `//nodejs` scripts do not run with Bun's globals on Windmill, and their
// environment is the native boundary's (C3n).
func EnvRewrite(f Format) (define map[string]string, support string, err error) {
	define = map[string]string{
		"Bun":            EnvBinding,
		"Bun.env":        EnvBinding + ".env",
		"globalThis.Bun": EnvBinding,
		"global.Bun":     EnvBinding,
		"self.Bun":       EnvBinding,
	}
	switch f {
	case ESM:
		define["import.meta.env"] = EnvBinding + ".env"
	case CJS:
	default:
		return nil, "", fmt.Errorf("bundle: unknown format %q", f)
	}
	return define, envSupport, nil
}

// envSupport defines EnvBinding. The proxy's target is an empty object,
// because the real Bun's properties are read-only and non-configurable: a
// proxy of Bun itself could not answer `env` with another object. Writes
// and deletes go to the real Bun, so they fail as they would there.
// Preventing extensions is refused (an empty target that stopped being
// extensible could no longer report Bun's keys), so freezing or sealing the
// proxied Bun throws, as does defining a non-configurable property on it.
// It reads the real Bun with Reflect.get, since `globalThis.Bun` would be
// rewritten itself, and defines EnvBinding as a non-enumerable, read-only
// global the first time any bundle in the process loads.
const envSupport = `// windmill-lite: the env rewrite's support module (ADR 0010 step 5).
const name = "` + EnvBinding + `";
if (Reflect.getOwnPropertyDescriptor(globalThis, name) === undefined) {
  const real = Reflect.get(globalThis, "Bun");
  Reflect.defineProperty(globalThis, name, {
    value:
      real === undefined
        ? undefined
        : new Proxy(Object.create(null), {
            get: (_, key) => (key === "env" ? process.env : Reflect.get(real, key)),
            set: (_, key, value) => {
              real[key] = value;
              return true;
            },
            has: (_, key) => Reflect.has(real, key),
            ownKeys: () => Reflect.ownKeys(real),
            getOwnPropertyDescriptor: (_, key) => {
              const d = Reflect.getOwnPropertyDescriptor(real, key);
              if (d === undefined) return undefined;
              if (key === "env") d.value = process.env;
              d.configurable = true;
              return d;
            },
            defineProperty: (_, key, d) => Reflect.defineProperty(real, key, d),
            deleteProperty: (_, key) => delete real[key],
            getPrototypeOf: () => Reflect.getPrototypeOf(real),
            preventExtensions: () => false,
          }),
  });
}
`
