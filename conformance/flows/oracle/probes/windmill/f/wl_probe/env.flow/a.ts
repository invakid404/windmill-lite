// Returns Bun's version and every WM_ variable, the token masked.
export async function main() {
  const env: Record<string, string> = {};
  for (const k of Object.keys(process.env).sort()) {
    if (k.startsWith("WM_")) env[k] = k === "WM_TOKEN" ? "<token>" : process.env[k]!;
  }
  return { bun: Bun.version, env };
}
