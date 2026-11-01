// The Bun side of internal/harness's boundary benchmarks (bench_test.go,
// milestone G1, ADR 0005's benchmark plan item 2). It uses the harness
// runtime's own frame codec and serialiser, so it measures the code a
// step's invocation runs, and nothing else:
//
//   bun bench.ts codec <invoke payload file> <result body file> <n>
//     The harness's parse and serialise of one invocation, n times after a
//     warm-up: decode the invoke frame's payload (UTF-8 and JSON.parse, as
//     the runtime reads arguments, WR-9), serialise the argument it carries
//     back as the result (the wrapper's resultText, WR-13), and encode the
//     result frame's payload around it. Prints one JSON line of
//     nanoseconds per operation for each phase.
//
//   bun bench.ts echo <result body file>
//     The transport alone, on file descriptor 3, a stream socket: it reads
//     frames with the runtime's FrameReader and reassembles part streams
//     with its Assembler, and answers each invoke, without decoding it,
//     with a result frame whose body is the file's text, encoded once at
//     start for each id, split into part frames when it is larger than a
//     frame, as the runtime's Sender splits it. The invoke's id is read
//     from its canonical prefix ({"t":"invoke","id":"...").

import { readFileSync } from "node:fs";
import { Assembler, decode, decodeFrame, encode, frame, FrameReader, MAX_FRAME, PART_CHUNK, Raw, split } from "../../../harnesses/bun/runtime/frame.ts";
import { resultText } from "../../../harnesses/bun/runtime/wrapper.ts";

const [mode, ...args] = process.argv.slice(2);

if (mode === "codec") {
  const invoke = new Uint8Array(readFileSync(args[0]));
  const body = readFileSync(args[1], "utf8");
  const n = Number(args[2]);
  // The result the step returns: its argument, as the echo step does.
  let value: unknown;
  const once = () => {
    const t0 = Bun.nanoseconds();
    const m = decode(invoke);
    const t1 = Bun.nanoseconds();
    const text = resultText(m.args.p);
    const t2 = Bun.nanoseconds();
    const out = encode({ t: "result", id: m.id, outcome: "ok", body: new Raw(text), settled: true });
    const t3 = Bun.nanoseconds();
    value = out;
    return [t1 - t0, t2 - t1, t3 - t2];
  };
  if (resultText(decode(invoke).args.p) !== body) throw new Error("the invoke's argument does not serialise to the result body");
  for (let i = 0; i < Math.min(200, Math.max(10, n)); i++) once();
  const sum = [0, 0, 0];
  for (let i = 0; i < n; i++) {
    const d = once();
    for (let k = 0; k < 3; k++) sum[k] += d[k];
  }
  void value;
  console.log(JSON.stringify({ n, decode_invoke_ns: sum[0] / n, result_text_ns: sum[1] / n, encode_result_ns: sum[2] / n }));
} else if (mode === "echo") {
  const body = readFileSync(args[0], "utf8");
  // The answers, encoded once per id: the echo measures moving them.
  const answers = new Map<string, Uint8Array>();
  const answer = (id: string) => {
    let a = answers.get(id);
    if (!a) answers.set(id, (a = encode({ t: "result", id, outcome: "ok", body: new Raw(body), settled: true })));
    return a;
  };
  const reader = new FrameReader();
  const assembler = new Assembler();
  const decoder = new TextDecoder();
  const prefix = '{"t":"invoke","id":"';
  const partPrefix = '{"t":"part"';
  let streams = 0;
  let sock: { write(b: Uint8Array): number } | undefined;
  const out: Uint8Array[] = [];
  let current: Uint8Array | null = null;
  const flush = () => {
    for (;;) {
      if (!current) current = out.shift() ?? null;
      if (!current) return;
      const n = sock!.write(current);
      if (n < 0) process.exit(0);
      if (n < current.length) {
        current = current.subarray(n);
        return;
      }
      current = null;
    }
  };
  const reply = (payload: Uint8Array) => {
    const head = decoder.decode(payload.subarray(0, 64));
    if (!head.startsWith(prefix)) throw new Error(`not an invoke: ${head}`);
    const a = answer(head.slice(prefix.length, head.indexOf('"', prefix.length)));
    if (a.length <= MAX_FRAME) out.push(frame(a));
    else for (const p of split(`e${++streams}`, a, PART_CHUNK)) out.push(frame(p));
    flush();
  };
  await Bun.connect({
    fd: 3,
    socket: {
      open(s) {
        sock = s;
      },
      data(s, data) {
        sock = s;
        for (const payload of reader.push(data)) {
          if (decoder.decode(payload.subarray(0, partPrefix.length)) !== partPrefix) {
            reply(payload);
            continue;
          }
          const full = assembler.add(decodeFrame(payload, assembler.maxMessage));
          if (full) reply(full);
        }
      },
      drain() {
        flush();
      },
      close() {
        process.exit(0);
      },
      error() {
        process.exit(1);
      },
    },
  } as unknown as Bun.UnixSocketOptions);
} else {
  console.error("usage: bun bench.ts codec <invoke file> <body file> <n> | echo <body file>");
  process.exit(2);
}
