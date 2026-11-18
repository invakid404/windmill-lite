// A stand-in for the engine in the host SDK's option tests (host.test.ts):
// it writes the arguments connect() started it with, as a JSON array, to
// the file WL_TEST_ARGS_FILE names, and the WL_REDIS_URL of its
// environment (null when unset), as JSON, to the file WL_TEST_ENV_FILE
// names when that is set; and exits without welcoming the host.
import { writeFileSync } from "node:fs";

writeFileSync(process.env.WL_TEST_ARGS_FILE!, JSON.stringify(process.argv.slice(2)));
if (process.env.WL_TEST_ENV_FILE) writeFileSync(process.env.WL_TEST_ENV_FILE, JSON.stringify(process.env.WL_REDIS_URL ?? null));
process.exit(3);
