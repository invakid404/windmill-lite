import * as client from "pkg-reexport";
export async function main() {
  return { job: client.JobService.getJob("j2"), flow: client.FlowService.getFlow("f/x"), names: Object.keys(client) };
}
