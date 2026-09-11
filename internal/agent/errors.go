package agent

import (
	"errors"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/errordetails"
)

// errorCatalog pairs the SDK error sentinels with their JSON-RPC codes and
// short display names, mirroring the spec's error table (§ error handling)
// and the SDK's internal code mapping.
var errorCatalog = []struct {
	sentinel error
	code     int
	name     string
}{
	{a2a.ErrParseError, -32700, "ParseError"},
	{a2a.ErrInvalidRequest, -32600, "InvalidRequest"},
	{a2a.ErrMethodNotFound, -32601, "MethodNotFound"},
	{a2a.ErrInvalidParams, -32602, "InvalidParams"},
	{a2a.ErrInternalError, -32603, "InternalError"},
	{a2a.ErrServerError, -32000, "ServerError"},
	{a2a.ErrTaskNotFound, -32001, "TaskNotFound"},
	{a2a.ErrTaskNotCancelable, -32002, "TaskNotCancelable"},
	{a2a.ErrPushNotificationNotSupported, -32003, "PushNotificationNotSupported"},
	{a2a.ErrUnsupportedOperation, -32004, "UnsupportedOperation"},
	{a2a.ErrUnsupportedContentType, -32005, "UnsupportedContentType"},
	{a2a.ErrInvalidAgentResponse, -32006, "InvalidAgentResponse"},
	{a2a.ErrExtendedCardNotConfigured, -32007, "ExtendedCardNotConfigured"},
	{a2a.ErrExtensionSupportRequired, -32008, "ExtensionSupportRequired"},
	{a2a.ErrVersionNotSupported, -32009, "VersionNotSupported"},
	{a2a.ErrUnauthenticated, -31401, "Unauthenticated"},
	{a2a.ErrUnauthorized, -31403, "Unauthorized"},
}

// FriendlyError renders err for the transcript. Protocol errors come out
// as "Name (-code) [REASON]: server message"; anything else falls back to
// the plain error text. All output is single-line (newlines collapsed).
func FriendlyError(err error) string {
	if err == nil {
		return ""
	}
	for _, e := range errorCatalog {
		if !errors.Is(err, e.sentinel) {
			continue
		}
		head := fmt.Sprintf("%s (%d)", e.name, e.code)
		if reason := ErrorReason(err); reason != "" {
			head += " [" + reason + "]"
		}
		msg := serverMessage(err, e.sentinel)
		if msg != "" {
			head += ": " + msg
		}
		return head
	}
	return collapseLines(err.Error())
}

// ErrorCode returns the JSON-RPC code for err, or 0 when err is not a
// recognized A2A/JSON-RPC error.
func ErrorCode(err error) int {
	for _, e := range errorCatalog {
		if errors.Is(err, e.sentinel) {
			return e.code
		}
	}
	return 0
}

// ErrorReason extracts the google.rpc.ErrorInfo reason the agent attached
// to the error ("TASK_NOT_FOUND", ...), or "" when absent.
func ErrorReason(err error) string {
	var a2aErr *a2a.Error
	if !errors.As(err, &a2aErr) {
		return ""
	}
	info := a2aErr.ErrorInfo()
	if info == nil {
		return ""
	}
	reason, _ := info.Value["reason"].(string)
	return reason
}

// ErrorDetailMeta returns the ErrorInfo metadata map, or nil. It can carry
// agent-specific context (timestamps, hints).
func ErrorDetailMeta(err error) map[string]string {
	var a2aErr *a2a.Error
	if !errors.As(err, &a2aErr) {
		return nil
	}
	for _, d := range a2aErr.TypedDetails {
		if d == nil || d.TypeURL != errordetails.ErrorInfoType {
			continue
		}
		if m, ok := d.Value["metadata"].(map[string]any); ok {
			out := make(map[string]string, len(m))
			for k, v := range m {
				if s, ok := v.(string); ok {
					out[k] = s
				}
			}
			return out
		}
	}
	return nil
}

// serverMessage picks the human-readable message for an A2A error,
// skipping the sentinel's own generic text.
func serverMessage(err error, sentinel error) string {
	var a2aErr *a2a.Error
	if errors.As(err, &a2aErr) && a2aErr.Message != "" && a2aErr.Message != sentinel.Error() {
		return collapseLines(a2aErr.Message)
	}
	return ""
}

// collapseLines flattens an arbitrary error string to one line.
func collapseLines(s string) string {
	return sanitize(s)
}
