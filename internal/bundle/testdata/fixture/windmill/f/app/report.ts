import { store } from "/f/shared/ctx.ts";
import { label } from "./format.ts";

export async function main(count: number, when: Date) {
  return { text: label(String(store.get("last"))), count, when: String(when) };
}
