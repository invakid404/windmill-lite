package platform

import "fmt"

// Error is a variant of Windmill's API error type (WA-42): its status and
// its display string, which the API sends as a text/plain body. The
// services answer their failures with it, the facade renders it as a
// reply, and worker argument resolution quotes its display as a cause
// (JE-42).
type Error struct {
	// Variant is the variant's name, one of the keys of errorVariants.
	Variant string
	// Message is the variant's payload: its message, or for the variants
	// Windmill renders raw (AIError, ExecutionErr, ResultTooLarge,
	// AlreadyCompleted, Generic) the whole body.
	Message string
	// Status is the status of a Generic error, which carries its own.
	Status int
}

// errorVariants maps each variant to its status and the format of its
// display (WA-42). The variants that Windmill renders with a Rust source
// location (SqlErr, the error kinds, InternalErrLoc, Anyhow) are written
// without it, as every message windmill-lite generates in place of
// Windmill's is (D14); JsonErr's payload rendering and the fixed sentences
// of WorkersAreBehind, MigrationNeeded and FeatureUnavailable are left to
// the milestone that first answers them.
var errorVariants = map[string]struct {
	status int
	format string
}{
	"NotFound":             {404, "Not found: %s"},
	"NotAuthorized":        {401, "Not authorized: %s"},
	"PermissionDenied":     {403, "Permission denied: %s"},
	"RequireAdmin":         {403, "Require Admin privileges for %s"},
	"BadRequest":           {400, "Bad request: %s"},
	"QuotaExceeded":        {400, "Quota exceeded: %s"},
	"SqlErr":               {400, "SqlErr: %s"},
	"AIError":              {400, "%s"},
	"BadGateway":           {502, "Bad gateway: %s"},
	"BadConfig":            {500, "Bad config: %s"},
	"ConnectingToDatabase": {500, "Connecting to database: %s"},
	"MetricNotFound":       {500, "Metric not found: %s"},
	"ExecutionErr":         {500, "%s"},
	"ResultTooLarge":       {500, "%s"},
	"IoErr":                {500, "IoErr: %s"},
	"Utf8Err":              {500, "Utf8Err: %s"},
	"UuidErr":              {500, "UuidErr: %s"},
	"SerdeJson":            {500, "SerdeJson: %s"},
	"HexErr":               {500, "HexErr: %s"},
	"InternalErr":          {500, "Internal: %s"},
	"InternalErrLoc":       {500, "Internal: %s"},
	"DatabaseMigration":    {500, "Migrating database: %s"},
	"ExecutionRawError":    {500, "ExecutionRawError: %s"},
	"Anyhow":               {500, "Error: %s"},
	"AlreadyCompleted":     {500, "%s"},
	"WacSuspended":         {500, "WAC job suspended: %s"},
	"FindPythonError":      {500, "Find python error: %s"},
	"ArgumentErr":          {500, "Problem with arguments: %s"},
}

// HTTPStatus is the status the API answers the error with.
func (e Error) HTTPStatus() int {
	if e.Variant == "Generic" {
		return e.Status
	}
	if v, ok := errorVariants[e.Variant]; ok {
		return v.status
	}
	return 500
}

// Body is the error's display string, the API's text/plain body.
func (e Error) Body() string {
	if e.Variant == "Generic" {
		return e.Message
	}
	v, ok := errorVariants[e.Variant]
	if !ok {
		return "Internal: windmill-lite: unknown error variant " + e.Variant + ": " + e.Message
	}
	return fmt.Sprintf(v.format, e.Message)
}

func (e *Error) Error() string { return e.Body() }

// notFound and internal are the two variants the services raise.
func notFound(format string, args ...any) *Error {
	return &Error{Variant: "NotFound", Message: fmt.Sprintf(format, args...)}
}

func internal(format string, args ...any) *Error {
	return &Error{Variant: "InternalErr", Message: fmt.Sprintf(format, args...)}
}
