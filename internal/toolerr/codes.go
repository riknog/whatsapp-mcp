package toolerr

// Error codes from docs/02-TOOLS.md. Add a new code there before adding it here.
const (
	CodeNotLoggedIn         Code = "not_logged_in"
	CodeDisconnected        Code = "disconnected"
	CodeContactNotFound     Code = "contact_not_found"
	CodeAmbiguousContact    Code = "ambiguous_contact"
	CodePhoneNotAllowed     Code = "phone_not_allowed"
	CodeChatHidden          Code = "chat_hidden"
	CodeContactNotShareable Code = "contact_not_shareable"
	CodeShareDisabled       Code = "share_disabled"
	CodeGroupSendDisabled   Code = "group_send_disabled"
	CodePolicyReplyOnly     Code = "policy_reply_only"
	CodeRateLimited         Code = "rate_limited"
	CodeQueueFull           Code = "queue_full"
	CodeDuplicateMessage    Code = "duplicate_message"
	CodeQuietHours          Code = "quiet_hours"
	CodeSendDisabled        Code = "send_disabled"
	CodeMessageTooLong      Code = "message_too_long"
	CodeInvalidArgument     Code = "invalid_argument"
	CodeSendFailed          Code = "send_failed"
	CodeSendUncertain       Code = "send_uncertain"
)

// AllCodes lists every code defined in this package, in doc order.
func AllCodes() []Code {
	return []Code{
		CodeNotLoggedIn,
		CodeDisconnected,
		CodeContactNotFound,
		CodeAmbiguousContact,
		CodePhoneNotAllowed,
		CodeChatHidden,
		CodeContactNotShareable,
		CodeShareDisabled,
		CodeGroupSendDisabled,
		CodePolicyReplyOnly,
		CodeRateLimited,
		CodeQueueFull,
		CodeDuplicateMessage,
		CodeQuietHours,
		CodeSendDisabled,
		CodeMessageTooLong,
		CodeInvalidArgument,
		CodeSendFailed,
		CodeSendUncertain,
	}
}
