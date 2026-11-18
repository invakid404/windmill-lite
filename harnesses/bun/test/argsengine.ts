// A stand-in for the engine in the host SDK's option tests (host.test.ts):
// it writes the arguments connect() started it with, as a JSON array, to
// the file WL_TEST_ARGS_FILE names; when WL_TEST_ENV_FILE is set, it
// writes there whether its WL_REDIS_URL is set and whether it equals
// WL_TEST_EXPECT_URL, never the URL itself; and it exits without welcoming
// the host.
import { writeFileSync } from "node:fs";

writeFileSync(process.env.WL_TEST_ARGS_FILE!, JSON.stringify(process.argv.slice(2)));
if (process.env.WL_TEST_ENV_FILE) {
  const url = process.env.WL_REDIS_URL;
  writeFileSync(process.env.WL_TEST_ENV_FILE, JSON.stringify({ set: url !== undefined, expected: url !== undefined && url === process.env.WL_TEST_EXPECT_URL }));
}
process.exit(3);
