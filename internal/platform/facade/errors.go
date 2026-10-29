package facade

import "github.com/invakid404/windmill-lite/internal/platform"

// Error is a variant of the API's error type as Windmill renders it
// (WA-42): text/plain, the variant's display string as the body, with the
// variant's status. Handlers answer their errors with it (Reply). The
// variants, their statuses and their displays are platform.Error's, which
// the services raise.
type Error platform.Error

// Reply renders the error as Windmill's API does.
func (e Error) Reply() Reply {
	p := platform.Error(e)
	return text(p.HTTPStatus(), p.Body())
}

func (e Error) Error() string { return platform.Error(e).Body() }
