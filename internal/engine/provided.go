package engine

import (
	"context"
	"fmt"

	"github.com/invakid404/windmill-lite/internal/ir"
	"github.com/invakid404/windmill-lite/internal/jsonval"
)

// provide answers the launch of a host-provided site (ADR 0002, ADR 0008)
// with the site's stored arguments: a static provider's value, verbatim,
// or the named provider's outcome (Config.Provide), which stands in for
// the job its target would have run; the completion phase then applies to
// it as to a real module's. A mandatory provider cannot decline: one that
// does, or a named site with no Provide, fails the run closed with
// ErrProviderDeclined, which the caller latches, as it latches the
// provider's own error.
func (r *run) provide(ctx context.Context, s Site, p *ir.Provided, args jsonval.Value) (StepOutcome, error) {
	if p.Static != nil {
		return StepOutcome{Kind: OutcomeOK, Body: jsonval.FromProducer(p.Static)}, nil
	}
	if r.e.provide != nil {
		out, ok, err := r.e.provide(ctx, r, s, p.Provider, args)
		switch {
		case err != nil:
			return StepOutcome{}, err
		case ok && (out.Kind == OutcomeOK || out.Kind == OutcomeError):
			return out, nil
		case ok:
			return StepOutcome{}, fmt.Errorf("engine: the provider %q of the site %s answered neither a result nor an error", p.Provider, s.Path)
		}
	}
	return StepOutcome{}, fmt.Errorf("%w: the provider %q of the site %s declined", ErrProviderDeclined, p.Provider, s.Path)
}
