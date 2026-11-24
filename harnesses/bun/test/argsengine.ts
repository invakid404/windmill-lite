// A stand-in for the engine in the host SDK's option tests (host.test.ts):
// it writes the arguments connect() started it with, as a JSON array, to
// the file WL_TEST_ARGS_FILE names; when WL_TEST_ENV_FILE is set, it
// writes there whether its WL_REDIS_URL is set and whether it equals
// WL_TEST_EXPECT_URL, never the URL itself; when WL_TEST_IDENTITY_FILE is
// set, it copies there the file its -identity flag names; and it exits
// without welcoming the host.
import { readFileSync, writeFileSync } from "node:fs";

writeFileSync(process.env.WL_TEST_ARGS_FILE!, JSON.stringify(process.argv.slice(2)));
// The identity file that -identity names, which connect removes once the
// engine has answered, copied for the test.
const identityAt = process.argv.indexOf("-identity");
if (process.env.WL_TEST_IDENTITY_FILE && identityAt >= 0) {
  writeFileSync(process.env.WL_TEST_IDENTITY_FILE, readFileSync(process.argv[identityAt + 1]));
}
if (process.env.WL_TEST_ENV_FILE) {
  const url = process.env.WL_REDIS_URL;
  writeFileSync(process.env.WL_TEST_ENV_FILE, JSON.stringify({ set: url !== undefined, expected: url !== undefined && url === process.env.WL_TEST_EXPECT_URL }));
}
process.exit(3);
