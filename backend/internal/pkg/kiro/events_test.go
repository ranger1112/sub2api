package kiro

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro/eventstream"
)

func kiroFrame(msgType, eventType, payload string) *eventstream.Frame {
	h := eventstream.Headers{}
	if msgType != "" {
		h[":message-type"] = eventstream.HeaderValue{Type: eventstream.HeaderString, Str: msgType}
	}
	if eventType != "" {
		h[":event-type"] = eventstream.HeaderValue{Type: eventstream.HeaderString, Str: eventType}
	}
	return &eventstream.Frame{Headers: h, Payload: []byte(payload)}
}

func TestEventFromFrame_AssistantResponse(t *testing.T) {
	ev := EventFromFrame(kiroFrame("event", "assistantResponseEvent", `{"content":"Hello","messageStatus":"COMPLETED"}`))
	if ev.Kind != EventAssistantResponse || ev.Content != "Hello" {
		t.Fatalf("got %+v", ev)
	}
}

func TestEventFromFrame_ToolUse(t *testing.T) {
	ev := EventFromFrame(kiroFrame("event", "toolUseEvent", `{"name":"read_file","toolUseId":"tu_1","input":"{\"p\":1}","stop":true}`))
	if ev.Kind != EventToolUse || ev.ToolUse.Name != "read_file" || ev.ToolUse.ToolUseID != "tu_1" || !ev.ToolUse.Stop {
		t.Fatalf("got %+v", ev)
	}
}

func TestEventFromFrame_ContextUsage(t *testing.T) {
	ev := EventFromFrame(kiroFrame("event", "contextUsageEvent", `{"contextUsagePercentage":42.5}`))
	if ev.Kind != EventContextUsage || ev.ContextUsagePercentage != 42.5 {
		t.Fatalf("got %+v", ev)
	}
}

func TestEventFromFrame_Metering(t *testing.T) {
	ev := EventFromFrame(kiroFrame("event", "meteringEvent", `{"unit":"credit","unitPlural":"credits","usage":0.34}`))
	if ev.Kind != EventMetering || ev.MeteringUsage != 0.34 || ev.MeteringUnit != "credit" {
		t.Fatalf("got %+v", ev)
	}
}

func TestEventFromFrame_ErrorAndException(t *testing.T) {
	h := eventstream.Headers{
		":message-type": {Type: eventstream.HeaderString, Str: "error"},
		":error-code":   {Type: eventstream.HeaderString, Str: "ThrottlingException"},
	}
	ev := EventFromFrame(&eventstream.Frame{Headers: h, Payload: []byte("rate limited")})
	if ev.Kind != EventError || ev.ErrorCode != "ThrottlingException" || ev.Message != "rate limited" {
		t.Fatalf("error frame: %+v", ev)
	}

	h2 := eventstream.Headers{
		":message-type":   {Type: eventstream.HeaderString, Str: "exception"},
		":exception-type": {Type: eventstream.HeaderString, Str: "ContentLengthExceededException"},
	}
	ev2 := EventFromFrame(&eventstream.Frame{Headers: h2, Payload: []byte("too long")})
	if ev2.Kind != EventException || ev2.ExceptionType != "ContentLengthExceededException" {
		t.Fatalf("exception frame: %+v", ev2)
	}
}

func TestEventFromFrame_UnknownAndMetering(t *testing.T) {
	if ev := EventFromFrame(kiroFrame("event", "somethingNew", `{}`)); ev.Kind != EventUnknown {
		t.Fatalf("unknown event type: %+v", ev)
	}
	if ev := EventFromFrame(kiroFrame("event", "meteringEvent", `{}`)); ev.Kind != EventMetering {
		t.Fatalf("metering: %+v", ev)
	}
}
