// Returns every WM_ variable, the token masked, and whether
// WM_END_USER_EMAIL and WM_ROOT_WORKSPACE equal WM_EMAIL and WM_WORKSPACE,
// whose values the comparison masks. Bun's version is probe bun_version.
export async function main() {
  const env: Record<string, string> = {};
  for (const k of Object.keys(process.env).sort()) {
    if (k.startsWith("WM_")) env[k] = k === "WM_TOKEN" ? "<token>" : process.env[k]!;
  }
  return {
    env,
    end_user_is_email: env.WM_END_USER_EMAIL === env.WM_EMAIL,
    root_workspace_is_workspace: env.WM_ROOT_WORKSPACE === env.WM_WORKSPACE,
  };
}
