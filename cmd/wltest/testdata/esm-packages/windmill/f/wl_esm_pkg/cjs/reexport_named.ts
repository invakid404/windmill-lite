import { JobService, settings } from "pkg-reexport";
export async function main() {
  return { job: JobService.getJob("j1"), base: settings.base };
}
