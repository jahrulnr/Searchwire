package searchwire

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// parseRetryAfter parses a Retry-After header value. It accepts either an
// integer number of seconds or an HTTP-date. Returns the seconds to wait and
// an error if the value cannot be parsed.
func parseRetryAfter(value string) (int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errEmptyRetryAfter
	}
	if n, err := strconv.Atoi(value); err == nil {
		if n < 0 {
			return 0, errNegativeRetryAfter
		}
		return n, nil
	}
	// HTTP-date form: Fri, 31 Dec 2023 23:59:59 GMT
	layouts := []string{
		"Mon, 02 Jan 2006 15:04:05 GMT",
		time.RFC1123,
		time.RFC850,
		time.ANSIC,
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			d := int(time.Until(t).Seconds())
			if d < 0 {
				return 0, nil
			}
			return d, nil
		}
	}
	return 0, errUnparseableRetryAfter
}

// parseJSONErrorEnvelope tries to extract a structured error body from a JSON
// response. Recognized shapes:
//   - {"error": "..."} or {"error": {"message": "..."}}
//   - {"message": "..."}
//   - {"detail": "..."}
//   - {"errors": [ ... ]}
//
// Returns the parsed top-level map (with nested values intact) so the agent
// can read whichever field the server used. Returns nil if the body is not
// valid JSON or not an object.
func parseJSONErrorEnvelope(body []byte) map[string]any {
	var v map[string]any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil
	}
	// Only treat as an error envelope if it carries at least one recognized
	// key; otherwise it might be a non-error JSON body served with a bad
	// status by mistake, and we still surface it but mark it as parsed.
	for _, key := range []string{"error", "errors", "message", "detail"} {
		if _, ok := v[key]; ok {
			return v
		}
	}
	return v
}
