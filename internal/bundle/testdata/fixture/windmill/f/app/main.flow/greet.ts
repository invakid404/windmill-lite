import { store } from "/f/shared/ctx.ts";
import { label } from "../format.ts";

export async function main(name: string) {
  store.set("last", name);
  return label("hello, " + name);
}
