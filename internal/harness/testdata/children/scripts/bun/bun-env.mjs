// Returns the Bun cache directory and NODE_PATH the step sees (WR-22/b).
export function main() {
  return [process.env.BUN_INSTALL_CACHE_DIR, process.env.NODE_PATH ?? "unset"];
}
