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

// PreludeBinding is the name the prelude binds and the define table maps
// the global `Bun` to.
const PreludeBinding = "__windmill_lite_Bun"

// EnvRewrite returns the env rewrite of ADR 0010 step 5 for one output
// format: the define table both bundlers apply, and the prelude both put
// before the bundle (as a banner).
//
// A harness gives each invocation its own view of process.env (WA-31), but
// Bun.env is a separate, read-only object that the runtime cannot replace.
// So the build points every read of the environment through Bun at
// process.env:
//
//   - `Bun.env` (also as `Bun["env"]`) becomes `process.env`;
//   - `import.meta.env` becomes `process.env` in ESM output only. In
//     CommonJS output import.meta stays empty, as in the CLI's esbuild
//     bundles, so code reading import.meta.env fails there as on Windmill;
//   - every other reference to the global `Bun`, also as `globalThis.Bun`,
//     `global.Bun` or `self.Bun` (so an alias such as `const B = Bun` and
//     destructuring such as `const { env } = Bun`), becomes the prelude's
//     binding: a proxy of the real Bun whose `env` is process.env and which
//     forwards everything else.
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
func EnvRewrite(f Format) (define map[string]string, prelude string, err error) {
	define = map[string]string{
		"Bun":            PreludeBinding,
		"Bun.env":        "process.env",
		"globalThis.Bun": PreludeBinding,
		"global.Bun":     PreludeBinding,
		"self.Bun":       PreludeBinding,
	}
	switch f {
	case ESM:
		define["import.meta.env"] = "process.env"
	case CJS:
	default:
		return nil, "", fmt.Errorf("bundle: unknown format %q", f)
	}
	return define, envPrelude, nil
}

// envPrelude binds PreludeBinding. The proxy's target is an empty object,
// because the real Bun's properties are read-only and non-configurable: a
// proxy of Bun itself could not answer `env` with another object. Writes
// and deletes go to the real Bun, so they fail as they would there.
// Preventing extensions is refused (an empty target that stopped being
// extensible could no longer report Bun's keys), so freezing or sealing the
// proxied Bun throws, as does defining a non-configurable property on it.
const envPrelude = `const ` + PreludeBinding + ` = (() => {
  const real = globalThis.Bun;
  if (real === undefined) return undefined;
  return new Proxy(Object.create(null), {
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
  });
})();`
