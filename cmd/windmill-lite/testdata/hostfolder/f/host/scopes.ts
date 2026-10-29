// The host-provided module of the host-mode tests: an AsyncLocalStorage
// that the host enters for each run. The host imports it and registers
// this instance, so that steps read the host's scope through it (ADR 0008
// H11, ADR 0010).
import { AsyncLocalStorage } from "node:async_hooks";

export const store = new AsyncLocalStorage<{ tenant: string }>();
