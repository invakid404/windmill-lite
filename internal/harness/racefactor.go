package harness

import "github.com/invakid404/windmill-lite/internal/racebuild"

// killAfterFactor stretches DefaultKillAfter in a binary built with -race
// (racebuild.Factor, 3; 1 in a plain build). The reader decodes a frame
// before it reads the next, and the watchdog counts that time as the
// harness's silence: under -race, decoding a 32 MiB platform request
// takes 11 to 15 s, while the harness's heartbeats wait in the socket.
const killAfterFactor = racebuild.Factor
