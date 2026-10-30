//native
import { Buffer } from "node:buffer";
import { readFileSync } from "node:fs";
export async function main() { return [Buffer.from("hi").toString("base64"), typeof readFileSync, typeof __dirname]; }
