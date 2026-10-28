package oracle

import (
	"context"
	"errors"
	"time"
)

// GiB is 2^30 bytes.
const GiB = 1 << 30

// The fail-closed disk policy of OR-30. The figures are floors, never
// lowered; a planning figure is guidance, not authorisation (step 8).
const (
	// MinFreeToPull is the free space below which no pull starts (step 3).
	MinFreeToPull uint64 = 8 * GiB
	// Reserve is the free space a pull must leave, and the floor below
	// which a pull or a run stops (steps 3, 4, 6 and 7).
	Reserve uint64 = 5 * GiB
	// TransientMargin covers in-flight compressed layers during a pull
	// (step 2).
	TransientMargin uint64 = 3 * GiB / 2
)

// imageDiskEstimate is the on-disk size assumed for an image that is not
// present yet (OR-30 step 2): the measured arm64 size of the Windmill image,
// three times the compressed amd64 size until the amd64 size is measured
// (Q-OR-2), and the measured size of the Postgres image for both.
func imageDiskEstimate(image, arch string) uint64 {
	switch image {
	case WindmillImage:
		if arch == "arm64" {
			return 3_940_469_084
		}
		return 3 * 1_373_000_000
	case PostgresImage:
		return 288_118_565
	}
	return 0
}

// PullNeed is the space a pull of the missing images needs: their on-disk
// estimates plus the transient margin (OR-30 step 2). It is zero when
// nothing is missing.
func PullNeed(missing []string, arch string) uint64 {
	if len(missing) == 0 {
		return 0
	}
	need := TransientMargin
	for _, m := range missing {
		need += imageDiskEstimate(m, arch)
	}
	return need
}

// CheckPull decides whether a pull may start (OR-30 step 3): free space is
// at least MinFreeToPull and leaves at least Reserve after need.
func CheckPull(free, need uint64) error {
	if free < MinFreeToPull || free < need || free-need < Reserve {
		return ErrInsufficientDisk(free, need, Reserve)
	}
	return nil
}

// FreeSpace measures free bytes on the filesystem that holds the Docker
// data root (OR-30 step 1).
type FreeSpace func() (uint64, error)

// Watch runs work under a disk watchdog: every interval it measures free
// space and cancels work's context, with ErrDiskFloor, once it falls below
// Reserve (OR-30 steps 4 and 7). A failed measurement also stops work: the
// policy fails closed. Watch returns work's error, or the watchdog's when
// it stopped work. It never retries or resumes (step 5).
func Watch(ctx context.Context, phase string, measure FreeSpace, interval time.Duration, work func(context.Context) error) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			free, err := measure()
			if err != nil {
				cancel(err)
				return
			}
			if free < Reserve {
				cancel(ErrDiskFloor(phase, free, Reserve))
				return
			}
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
	err := work(ctx)
	close(done)
	<-stopped
	if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return err
}

// SettledFree measures free space after a completed pull (OR-30 step 6).
// A first reading at or above Reserve is accepted. A lower reading may be
// transient, so the space is read again after settle; the second reading
// decides.
func SettledFree(ctx context.Context, measure FreeSpace, settle time.Duration) (uint64, error) {
	free, err := measure()
	if err != nil || free >= Reserve {
		return free, err
	}
	select {
	case <-ctx.Done():
		return free, ctx.Err()
	case <-time.After(settle):
	}
	return measure()
}
