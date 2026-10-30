import { writeS3File } from "windmill-client@1.767.0";
// The stock client's upload with no workspace storage configured; what comes
// back is the observation (the CE handler is closed source).
export async function main() {
  try { return { ok: await writeS3File({ s3: "t/a.txt" }, "hi") }; }
  catch (e) { return { error: String((e as Error).message ?? e) }; }
}
