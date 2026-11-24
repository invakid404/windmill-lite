// A Bun host for the bundled-plugin host tests of
// cmd/windmill-lite/host_test.go. It runs the scenario that WL_SCENARIO
// names, prints one JSON line of what it saw, and exits.
//
//   - reconnect: connects on WL_BUNDLE, runs the flows f/app/plain_flow and
//     f/app/native_flow, whose steps fetch WL_URL and return the header
//     x-plugin-wrap, and closes. With WL_HOST_WRAP=1 the host then wraps
//     globalThis.fetch itself, setting that header. It connects again, in
//     the same process, on WL_BUNDLE2 and runs both flows again.
//   - connect: connects on WL_BUNDLE and reports whether connect() resolved,
//     or the HostError's code and message. WL_ENGINE_FLAGS, split at
//     spaces, are its engineFlags, such as a -digest pin.
//   - releases: copies the bundle WL_RELEASE_A to WL_DIR, connects, runs
//     f/app/plain_flow and keeps its warnings, and closes; then replaces
//     WL_DIR with the bundle WL_RELEASE_B and connects again, in the same
//     process, reporting its warnings or the HostError's code and message.
//
// Environment: WL_ENGINE (the engine binary) and the scenario's variables.

import { cpSync, rmSync } from "node:fs";
import { connect, HostError } from "../../../../harnesses/bun/host/index.ts";

const env = process.env;
const args = { url: env.WL_URL ?? "http://127.0.0.1:9" };
type Host = Awaited<ReturnType<typeof connect>>;

const open = (bundle: string) =>
  connect({ engine: env.WL_ENGINE!, bundle, workdir: process.cwd(), engineFlags: env.WL_ENGINE_FLAGS ? env.WL_ENGINE_FLAGS.split(" ") : undefined });

async function result(host: Host, flow: string) {
  const r = await host.run(flow, args);
  return r.status === "success" ? r.result : { status: r.status, result: r.result };
}

// connect()'s outcome: the host, or the HostError's code and message.
async function attempt(bundle: string): Promise<Host | { code: string; message: string }> {
  try {
    return await open(bundle);
  } catch (e) {
    if (e instanceof HostError) return { code: e.code, message: e.message };
    throw e;
  }
}

let report: unknown;
switch (env.WL_SCENARIO) {
  case "reconnect": {
    const first = await open(env.WL_BUNDLE!);
    const plain = await result(first, "f/app/plain_flow");
    const native = await result(first, "f/app/native_flow");
    await first.close();
    if (env.WL_HOST_WRAP === "1") {
      const inner = globalThis.fetch;
      globalThis.fetch = Object.assign((input: RequestInfo | URL, init?: RequestInit) => {
        const req = new Request(input, init);
        req.headers.set("x-plugin-wrap", "host");
        return inner(req);
      }, inner);
    }
    const second = await open(env.WL_BUNDLE2!);
    const plainAgain = await result(second, "f/app/plain_flow");
    const nativeAgain = await result(second, "f/app/native_flow");
    await second.close();
    report = { plain, native, plainAgain, nativeAgain };
    break;
  }
  case "connect": {
    const got = await attempt(env.WL_BUNDLE!);
    if ("code" in got) report = got;
    else {
      await got.close();
      report = { connected: true };
    }
    break;
  }
  case "releases": {
    const dir = env.WL_DIR!;
    cpSync(env.WL_RELEASE_A!, dir, { recursive: true });
    const first = await open(dir);
    const r = await first.run("f/app/plain_flow", args);
    await first.close();
    rmSync(dir, { recursive: true });
    cpSync(env.WL_RELEASE_B!, dir, { recursive: true });
    const got = await attempt(dir);
    let second: unknown = got;
    if (!("code" in got)) {
      second = { warnings: (await got.run("f/app/plain_flow", args)).warnings };
      await got.close();
    }
    report = { first: r.warnings, second };
    break;
  }
  default:
    throw new Error(`unknown scenario ${env.WL_SCENARIO}`);
}
console.log(JSON.stringify(report));
