package quickjs

/*
#include "wl_quickjs.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"syscall"
)

// referenceCallStack is the C stack, in bytes, that the engine's stack
// limit counts for one call of a simple recursive function. How much C
// stack such a call really takes depends on the compiler that built the
// engine: on amd64, 784 bytes with GCC 13, 1 008 to 1 040 with clang and
// 1 728 with GCC 15, whose inliner doubles the interpreter's frame. The
// engine measures it when the process starts its first Engine
// ([CallStackBytes]) and scales the configured limit by it over this value,
// so that the recursion it measures fits about StackBytes / 768 calls on
// every build: about 1 365 at the default 1 MiB, a third more than the
// recursion of depth 1 000 that the engine fits where Windmill's overflows
// ([D17]). A function with more locals or arguments takes the same extra
// stack on every build, so its depth still varies a little with the build.
// Windmill's own embedding applies QuickJS-ng's 1 MiB unscaled ([JE-48]).
//
// [D17]: https://github.com/invakid404/windmill-lite/blob/main/docs/design/0001-equivalence-contract.md#5-named-deviations
// [JE-48]: https://github.com/invakid404/windmill-lite/blob/main/docs/semantics/expressions.md#je-48
const referenceCallStack = 768

// The probe measures the stack per call from the depth that fits under
// two limits, so that the frames between the limit's top and the first call
// cancel out. The 3.75 MiB between them hold over 2 000 calls on every
// build measured, so the slope is within 1 byte of the cost. The probe's
// thread holds threadStackSlack beyond the higher limit.
const (
	probeLowLimit  = 256 << 10
	probeHighLimit = 4 << 20
	probeThread    = probeHighLimit + threadStackSlack
)

// minCallStack and maxCallStack bound the stack per call that the engine
// accepts from the probe. The builds measured take 736 to 1 728 bytes; the
// bounds leave room for builds with larger frames, such as sanitizer
// builds, and refuse a broken measurement before it scales the default
// limit past 21 MiB (1 MiB × 16 KiB / 768 B) and every slot thread with it.
const (
	minCallStack = 64
	maxCallStack = 16 << 10
)

// callStack holds the stack per call once it has been measured: it is a
// property of the build, the same for every Engine.
var callStack struct {
	mu    sync.Mutex
	bytes int
}

// CallStackBytes is the C stack, in bytes, that one call of a simple
// recursive JavaScript function takes in this build of the engine: the
// slope of the depth that fits between a 256 KiB and a 4 MiB limit. The
// process measures it once, when it starts its first Engine. If the
// measurement fails, or gives less than 64 bytes or more than 16 KiB,
// CallStackBytes returns an error, New fails with it, and the next call
// measures again.
func CallStackBytes() (int, error) {
	callStack.mu.Lock()
	defer callStack.mu.Unlock()
	if callStack.bytes == 0 {
		n, err := measureCallStack()
		if err != nil {
			return 0, err
		}
		callStack.bytes = n
	}
	return callStack.bytes, nil
}

func measureCallStack() (int, error) {
	low, err := probeDepth(probeLowLimit)
	if err != nil {
		return 0, err
	}
	high, err := probeDepth(probeHighLimit)
	if err != nil {
		return 0, err
	}
	if low <= 0 || high <= low {
		return 0, fmt.Errorf("expr/quickjs: cannot measure the stack per call: depths %d and %d under %d and %d bytes", low, high, probeLowLimit, probeHighLimit)
	}
	return plausibleCallStack(int(math.Round(float64(probeHighLimit-probeLowLimit) / float64(high-low))))
}

// plausibleCallStack returns perCall, or an error naming it when it lies
// outside minCallStack to maxCallStack.
func plausibleCallStack(perCall int) (int, error) {
	if perCall < minCallStack || perCall > maxCallStack {
		return 0, fmt.Errorf("expr/quickjs: implausible stack per call: %d bytes, outside %d to %d", perCall, minCallStack, maxCallStack)
	}
	return perCall, nil
}

// probeDepth is the depth that the probe's recursion reaches under a stack
// limit of limit bytes.
func probeDepth(limit int) (int64, error) {
	var depth C.int64_t
	var status C.int
	if rc := C.wl_probe_depth(C.size_t(probeThread), C.size_t(limit), &depth, &status); rc != 0 {
		return 0, fmt.Errorf("expr/quickjs: cannot run the stack probe's thread: %w", syscall.Errno(rc))
	}
	switch status {
	case C.WL_OK:
		return int64(depth), nil
	case C.WL_NO_MEMORY:
		return 0, errPrepare
	}
	return 0, errors.New("expr/quickjs: the stack probe did not overflow with a RangeError")
}

// stackLimit is the limit, in bytes, that the engine gives QuickJS-ng for
// a configured StackBytes when one call takes perCall bytes: stackBytes ×
// perCall / referenceCallStack, rounded up, and never less than stackBytes,
// so that a build whose calls take less than the reference keeps at least
// the configured stack.
func stackLimit(stackBytes, perCall int) int {
	if perCall <= referenceCallStack {
		return stackBytes
	}
	if stackBytes > (math.MaxInt-referenceCallStack)/perCall {
		return math.MaxInt
	}
	return (stackBytes*perCall + referenceCallStack - 1) / referenceCallStack
}

// maxStackLimit is the largest stack limit whose slot thread
// (threadStack) has a size that an int can hold.
const maxStackLimit = math.MaxInt - threadStackSlack - threadStackUnit

// StackLimit is the stack limit, in bytes, that the Engine gives each
// evaluation: Limits().StackBytes scaled to this build's frames
// ([CallStackBytes], referenceCallStack). It is measured from where each
// call enters the engine.
func (e *Engine) StackLimit() int { return e.stackLimit }
