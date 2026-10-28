// windmill-lite: the env rewrite's support module (ADR 0010 step 5).
const name = "__windmill_lite_Bun";
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
