// Package toolerr defines the error type returned by MCP tools. Its codes match
// the table in docs/02-TOOLS.md.
package toolerr

import (
	"encoding/json"
	"fmt"
)

// Code is a stable, machine-readable error identifier returned to the model.
type Code string

// Error is a tool error. It serializes to {"code","message","details"}.
// Details never contains phone numbers or JIDs.
type Error struct {
	Code    Code
	Message string
	Details map[string]any
}

// New builds a tool error. A nil details map is allowed.
func New(code Code, message string, details map[string]any) Error {
	return Error{Code: code, Message: message, Details: details}
}

// Error implements the error interface.
func (e Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// MarshalJSON emits {"code","message","details"}. Details is always an object,
// even when empty, so clients can rely on the shape.
func (e Error) MarshalJSON() ([]byte, error) {
	details := e.Details
	if details == nil {
		details = map[string]any{}
	}
	return json.Marshal(struct {
		Code    Code           `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}{Code: e.Code, Message: e.Message, Details: details})
}
