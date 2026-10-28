package expr_test

import (
	"fmt"

	"github.com/invakid404/windmill-lite/internal/expr"
	"github.com/invakid404/windmill-lite/internal/expr/quickjs"
)

// The tests of package expr that run on the real engine (milestone B2b)
// get it from here: package expr cannot import internal/expr/quickjs, which
// imports it, but this external test package of the same test binary can.
func init() {
	expr.RegisterTestEngine(quickjs.CLibrary(), func(l expr.Limits, slots int) (expr.Engine, func() error, error) {
		e, err := quickjs.New(quickjs.Options{Limits: l, Slots: slots})
		if err != nil {
			return nil, nil, err
		}
		stop := func() error {
			e.Close()
			if n := e.Stats().LeakedBlocks; n != 0 {
				return fmt.Errorf("runtimes leaked %d heap blocks", n)
			}
			return nil
		}
		return e, stop, nil
	})
}
