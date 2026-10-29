// WR-47/e: the .txt loader makes the file a string.
// @ts-ignore: esbuild's text loader
import note from "./note.txt";
export function main() {
  return note;
}
