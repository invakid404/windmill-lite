// A host-provided module outside the sync folder (milestone F3h): the
// codebases entry ../hostlib of testdata/hostfolder holds it, its scripts
// import it from outside the folder, and the host contract lists it by its
// path from the sync root, ../hostlib/tenant.ts. The test host registers
// its own instance and enters its scope around each run.
import { AsyncLocalStorage } from "node:async_hooks";

export const tenant = new AsyncLocalStorage<string>();
