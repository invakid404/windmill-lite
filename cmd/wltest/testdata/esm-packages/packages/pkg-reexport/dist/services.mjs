import { settings } from "./core.mjs";
export class JobService {
  static getJob(id) { return { id, base: settings.base }; }
}
export class FlowService {
  static getFlow(path) { return { path, base: settings.base }; }
}
