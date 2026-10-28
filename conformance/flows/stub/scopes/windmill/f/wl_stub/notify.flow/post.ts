// Sends a message through a chat service that only the flow's owners can reach.
export async function main(to: string, team: string) {
  const res = await fetch("http://chat.invalid/send", { method: "POST", body: JSON.stringify({ to, team }) });
  return await res.json();
}
