// Looks a team up in a directory that only the flow's owners can reach.
export async function main(team: string) {
  const res = await fetch(`http://directory.invalid/teams/${encodeURIComponent(team)}`);
  return await res.json();
}
