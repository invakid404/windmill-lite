// Package harness is the engine side of the harness protocol (ADR 0004):
// frames and part streams, the handshake, spawned harness processes with
// their supervision and watchdog, and pools of them. Pool and Process
// implement engine.StepExecutor.
//
// The harness side for Bun lives in harnesses/bun/runtime. The two sides
// share the cases in conformance/protocol: the frame codec's golden frames,
// which both run, and exchange transcripts, which the Go tests play against
// the real Bun harness on the pinned Bun.
package harness
