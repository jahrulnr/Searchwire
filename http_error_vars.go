package searchwire

import "errors"

var (
	errEmptyRetryAfter       = errors.New("searchwire: empty Retry-After")
	errNegativeRetryAfter    = errors.New("searchwire: negative Retry-After")
	errUnparseableRetryAfter = errors.New("searchwire: unparseable Retry-After")
)
