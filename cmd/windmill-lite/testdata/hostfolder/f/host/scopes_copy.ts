// The same module under a path the host contract does not list: each
// script that imports it bundles its own copy, as Windmill inlines every
// import (WR-27), with an AsyncLocalStorage that no host enters.
import { AsyncLocalStorage } from "node:async_hooks";

export const store = new AsyncLocalStorage<{ tenant: string }>();
