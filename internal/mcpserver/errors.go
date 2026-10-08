package mcpserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	localcat "github.com/riknog/whatsapp-mcp/internal/category"
	"github.com/riknog/whatsapp-mcp/internal/toolerr"
)

// errBody is the error object of docs/02-TOOLS.md: {code, message, details}.
type errBody struct {
	Code    string         `json:"code" jsonschema:"stable error code from docs/02-TOOLS.md"`
	Message string         `json:"message"`
	Details map[string]any `json:"details"`
}

// errCarrier is implemented by every tool output, so that failed can set the error.
type errCarrier interface {
	setError(*errBody)
}

// failed turns a tool error into a result with isError set and a typed output
// that carries the error body. Any other error is returned unchanged; the SDK
// reports it as an error result as well.
func failed[Out any, P interface {
	*Out
	errCarrier
}](err error) (*mcp.CallToolResult, Out, error) {
	var out Out
	var te toolerr.Error
	if !errors.As(err, &te) {
		return nil, out, err
	}
	details := te.Details
	if details == nil {
		details = map[string]any{}
	}
	P(&out).setError(&errBody{Code: string(te.Code), Message: te.Message, Details: details})
	res := &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: string(te.Code) + ": " + te.Message}},
	}
	return res, out, nil
}

// list is a slice that always marshals as a JSON array, even when nil. Outputs
// are validated against their schema, which requires arrays, so an error output
// with empty fields must still be an array.
type list[T any] []T

// MarshalJSON writes [] for a nil list.
func (l list[T]) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]T(l))
}

// trimmed trims white space around a tool argument.
func trimmed(s string) string {
	return strings.TrimSpace(s)
}

// requireSession is the gate of every tool that reads WhatsApp data.
func (e *env) requireSession() error {
	if e.client.IsLoggedIn() {
		return nil
	}
	return toolerr.New(toolerr.CodeNotLoggedIn,
		"Sem sessão do WhatsApp. Rode `whatsapp-mcp login` no terminal.", nil)
}

// unknownCategory is the error for a category name that is not listed.
func unknownCategory() error { return localcat.Unknown() }

// requireContact is the error for an empty contact argument.
func requireContact(s string) error {
	if s == "" {
		return toolerr.New(toolerr.CodeInvalidArgument,
			"Informe o contato pelo nome ou pelo contact_ref.", nil)
	}
	return nil
}

// clampInt returns v with def for 0 or less and limit for larger values. A
// reduced value adds a note for the model, so the cut is never silent.
func clampInt(name string, v, def, limit int, notes *list[string]) int {
	switch {
	case v <= 0:
		return def
	case v > limit:
		*notes = append(*notes, reducedNote(name, v, limit))
		return limit
	default:
		return v
	}
}

// clampOffset returns v between 0 and limit, with a note when it is cut.
func clampOffset(name string, v, limit int, notes *list[string]) int {
	switch {
	case v < 0:
		return 0
	case v > limit:
		*notes = append(*notes, reducedNote(name, v, limit))
		return limit
	default:
		return v
	}
}

// maxEchoed is the largest requested value a note repeats. A larger one could
// be a phone number typed by the model, so the note leaves it out.
const maxEchoed = 9_999_999

func reducedNote(name string, v, limit int) string {
	if v > maxEchoed {
		return fmt.Sprintf("%s reduzido para o máximo de %d.", name, limit)
	}
	return fmt.Sprintf("%s reduzido de %d para o máximo de %d.", name, v, limit)
}
