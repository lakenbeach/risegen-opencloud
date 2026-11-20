package adapter

import (
	"errors"
	"strings"
)

// maxDetailLength caps the "detail" field carried by the JSON error envelope.
const maxDetailLength = 512

// RootCause returns a sanitized description of the deepest cause wrapped by an
// adapter error. The C bridge copies it to the additive "detail" field of the
// error envelope, so callers keep the stable "code" while still seeing why the
// operation failed.
//
// Credentials never reach the envelope: user-info and sensitive query
// parameters are masked, and the result is truncated to maxDetailLength.
func RootCause(err error) string {
	if err == nil {
		return ""
	}

	deepest := err
	for {
		next := errors.Unwrap(deepest)
		if next == nil {
			break
		}
		deepest = next
	}

	return SanitizeMessage(deepest.Error())
}

// SanitizeMessage masks credentials embedded in a free-form error message and
// flattens it to a single line of bounded length.
func SanitizeMessage(msg string) string {
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	msg = strings.Join(strings.Fields(msg), " ")
	msg = sanitizeURL(msg)
	if len(msg) > maxDetailLength {
		msg = msg[:maxDetailLength] + "..."
	}
	return msg
}
