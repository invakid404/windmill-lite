// Loads each shared-layout module named on the command line and reports how
// its own code treats module.exports, as JSON by file:
//
//   - assigned: how many times loading it assigns module.exports;
//   - main: the type of the main its exports then hold;
//   - error: what loading it threw, if it threw.
//
// A stand-in closure registry hands every dependency reference an empty
// object, so only the module's own code runs: none of its dependencies'.
// A CommonJS module (.cjs) runs with a module whose exports property counts
// assignments. An ES module (.mjs) is imported with a global module of the
// same kind, so that code reading a free module finds it rather than
// throwing a ReferenceError.
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname } from "node:path";

globalThis[Symbol.for("windmill-lite.closures")] = { instance: () => () => ({}) };

function counted() {
  const module = { assigned: 0 };
  let exports = {};
  Object.defineProperty(module, "exports", {
    get: () => exports,
    set: (v) => {
      module.assigned++;
      exports = v;
    },
  });
  return module;
}

const out = {};
for (const file of process.argv.slice(2)) {
  const module = counted();
  try {
    let main;
    if (file.endsWith(".cjs")) {
      const run = new Function("exports", "require", "module", "__filename", "__dirname", readFileSync(file, "utf8"));
      run(module.exports, createRequire(file), module, file, dirname(file));
      main = module.exports.main;
    } else {
      globalThis.module = module;
      try {
        main = (await import(file)).main;
      } finally {
        delete globalThis.module;
      }
    }
    out[file] = { assigned: module.assigned, main: typeof main };
  } catch (e) {
    out[file] = { assigned: module.assigned, error: String(e) };
  }
}
console.log(JSON.stringify(out));
