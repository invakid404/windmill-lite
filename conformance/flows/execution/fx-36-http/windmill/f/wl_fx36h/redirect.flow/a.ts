// FX-36: a status code that is a redirect, with the header a client
// follows; run-and-wait's response is the redirect itself.
export async function main() {
  return { windmill_status_code: 302, windmill_headers: { Location: "/elsewhere" }, result: "moved" };
}
