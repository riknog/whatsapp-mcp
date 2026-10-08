package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/riknog/whatsapp-mcp/internal/privacy"
)

type statusOut struct {
	Connected   bool     `json:"connected" jsonschema:"the socket to WhatsApp is up"`
	LoggedIn    bool     `json:"logged_in" jsonschema:"a session exists; false means run whatsapp-mcp login"`
	AccountName string   `json:"account_name"`
	AccountType string   `json:"account_type" jsonschema:"business or personal"`
	LastEventAt string   `json:"last_event_at,omitempty" jsonschema:"ISO-8601 time of the last event from WhatsApp"`
	Queue       queueOut `json:"queue"`
}

type queueOut struct {
	Pending      int       `json:"pending" jsonschema:"messages waiting to be sent"`
	SentLastHour int64     `json:"sent_last_hour"`
	Limits       limitsOut `json:"limits"`
}

type limitsOut struct {
	MaxPerMinute            int `json:"max_per_minute"`
	MaxPerHour              int `json:"max_per_hour"`
	MaxPerDay               int `json:"max_per_day"`
	MaxNewRecipientsPerHour int `json:"max_new_recipients_per_hour"`
	MaxQueue                int `json:"max_queue"`
}

// status never fails: it is the call that tells the model why nothing works.
func (e *env) status(_ context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, statusOut, error) {
	out := statusOut{
		Connected:   e.client.IsConnected(),
		LoggedIn:    e.client.IsLoggedIn(),
		AccountName: privacy.RedactText(e.client.AccountName()),
		AccountType: e.client.AccountType(),
		Queue: queueOut{Limits: limitsOut{
			MaxPerMinute:            e.cfg.Send.MaxPerMinute,
			MaxPerHour:              e.cfg.Send.MaxPerHour,
			MaxPerDay:               e.cfg.Send.MaxPerDay,
			MaxNewRecipientsPerHour: e.cfg.Send.MaxNewRecipientsPerHour,
			MaxQueue:                e.cfg.Send.MaxQueue,
		}},
	}
	if e.queue != nil {
		s := e.queue.Stats()
		out.Queue.Pending = s.Pending
		out.Queue.SentLastHour = s.SentLastHour
	}
	if e.lastEvent != nil {
		if t := e.lastEvent(); !t.IsZero() {
			out.LastEventAt = t.In(e.loc).Format("2006-01-02T15:04:05Z07:00")
		}
	}
	return nil, out, nil
}
