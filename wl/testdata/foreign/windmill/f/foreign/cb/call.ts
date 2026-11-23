// Calls the Windmill at base through the stock client, as call names, and
// returns its own WM_JOB_ID, WM_ROOT_FLOW_JOB_ID and WM_ROOT_JOB_ID, read
// first, with what the call returned. This directory is a codebase, so the build inlines
// the client from its node_modules, which the tests copy in.
import * as wmill from "windmill-client";

export async function main(base: string, call: string) {
  const job = process.env.WM_JOB_ID;
  const root = process.env.WM_ROOT_FLOW_JOB_ID;
  const top = process.env.WM_ROOT_JOB_ID;
  wmill.setClient("a-token", base);
  let out: unknown;
  switch (call) {
    case "runScriptAsync":
      out = await wmill.runScriptAsync("f/remote/script", null, { x: 1 }, null, "t1");
      break;
    case "runFlow":
      out = await wmill.runFlow("f/remote/flow", { x: 1 });
      break;
    case "runFlowAsync":
      out = await wmill.runFlowAsync("f/remote/flow", { x: 1 });
      break;
    case "otherParent":
      // A parent that the step names itself, which is no job id of its own.
      process.env.WM_JOB_ID = "0193c1e0-0000-7000-8000-0000000000aa";
      out = await wmill.runScriptAsync("f/remote/script", null, { x: 1 });
      break;
    case "topParent": {
      // A dispatch whose parent is the job at the top of the step's run, as
      // a step may name it itself; in a subflow that job is not the root.
      const r = await fetch(`${base}/api/w/${process.env.WM_WORKSPACE}/jobs/run/p/f/remote/script?parent_job=${top}&root_job=${root}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ x: 1 }),
      });
      out = await r.text();
      break;
    }
    case "outsideRun": {
      const r = await fetch(`${base}/api/w/${process.env.WM_WORKSPACE}/jobs/list?parent_job=${job}&root_job=${root}`);
      out = await r.json();
      break;
    }
    default:
      throw new Error(`no call ${call}`);
  }
  return { job, root, top, out };
}
