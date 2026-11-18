// A stand-in for the engine in the host SDK's option tests (host.test.ts):
// it writes the arguments connect() started it with, as a JSON array, to
// the file WL_TEST_ARGS_FILE names, and exits without welcoming the host.
import { writeFileSync } from "node:fs";

writeFileSync(process.env.WL_TEST_ARGS_FILE!, JSON.stringify(process.argv.slice(2)));
process.exit(3);
