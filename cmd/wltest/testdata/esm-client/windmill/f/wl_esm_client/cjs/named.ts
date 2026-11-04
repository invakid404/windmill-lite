import { JobService } from "windmill-client";
export async function main() {
  return typeof JobService.getJob;
}
