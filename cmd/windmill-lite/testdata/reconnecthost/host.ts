// A Bun host for the reconnect test of the native fetch
// (cmd/windmill-lite/host_test.go, TestHostReconnectKeepsNativeFetch). It
// connects the engine on WL_BUNDLE, whose own plugin wraps the global fetch
// when it loads, runs the flows f/app/plain_flow and f/app/native_flow, and
// closes; then it connects again, in the same process, on WL_BUNDLE2, which
// has no plugin, and runs f/app/native_flow again. Each flow's step fetches
// WL_URL and returns the header that the wrapper sets. It prints one JSON
// line of the three results, and exits.
//
// Environment: WL_ENGINE (the engine binary), WL_BUNDLE, WL_BUNDLE2, WL_URL.

import { connect } from "../../../../harnesses/bun/host/index.ts";

const env = process.env;
const args = { url: env.WL_URL! };

async function result(host: Awaited<ReturnType<typeof connect>>, flow: string) {
  const r = await host.run(flow, args);
  return r.status === "success" ? r.result : { status: r.status, result: r.result };
}

const first = await connect({ engine: env.WL_ENGINE!, bundle: env.WL_BUNDLE!, workdir: process.cwd() });
const plain = await result(first, "f/app/plain_flow");
const native = await result(first, "f/app/native_flow");
await first.close();

const second = await connect({ engine: env.WL_ENGINE!, bundle: env.WL_BUNDLE2!, workdir: process.cwd() });
const nativeAgain = await result(second, "f/app/native_flow");
await second.close();

console.log(JSON.stringify({ plain, native, nativeAgain }));
