package kiro

import (
	"encoding/json"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro/eventstream"
)

// EventKind 是从 AWS Event Stream 帧解析出的 Kiro 事件类型。
type EventKind int

const (
	EventUnknown EventKind = iota
	EventAssistantResponse
	EventToolUse
	EventMetering
	EventContextUsage
	EventError
	EventException
)

// Event 是一个已解码的 Kiro 事件。按 Kind 读取对应字段。
type Event struct {
	Kind EventKind

	Content string // AssistantResponse

	ToolUse ToolUseEvent // ToolUse

	ContextUsagePercentage float64 // ContextUsage

	ErrorCode     string // Error
	ExceptionType string // Exception
	Message       string // Error / Exception 的 payload
}

// ToolUseEvent 是 toolUseEvent 的 payload(input 为流式 JSON 片段)。
type ToolUseEvent struct {
	Name      string `json:"name"`
	ToolUseID string `json:"toolUseId"`
	Input     string `json:"input"`
	Stop      bool   `json:"stop"`
}

type assistantResponseEvent struct {
	Content string `json:"content"`
}

type contextUsageEvent struct {
	ContextUsagePercentage float64 `json:"contextUsagePercentage"`
}

// EventFromFrame 把一个帧解析为 Kiro 事件。payload 反序列化失败按空内容处理(不中断流)。
func EventFromFrame(frame *eventstream.Frame) Event {
	messageType := frame.MessageType()
	if messageType == "" {
		messageType = "event"
	}
	switch messageType {
	case "event":
		return parseEventFrame(frame)
	case "error":
		code := frame.Headers.ErrorCode()
		if code == "" {
			code = "UnknownError"
		}
		return Event{Kind: EventError, ErrorCode: code, Message: string(frame.Payload)}
	case "exception":
		et := frame.Headers.ExceptionType()
		if et == "" {
			et = "UnknownException"
		}
		return Event{Kind: EventException, ExceptionType: et, Message: string(frame.Payload)}
	default:
		return Event{Kind: EventUnknown}
	}
}

func parseEventFrame(frame *eventstream.Frame) Event {
	switch frame.EventType() {
	case "assistantResponseEvent":
		var p assistantResponseEvent
		_ = json.Unmarshal(frame.Payload, &p)
		return Event{Kind: EventAssistantResponse, Content: p.Content}
	case "toolUseEvent":
		var p ToolUseEvent
		_ = json.Unmarshal(frame.Payload, &p)
		return Event{Kind: EventToolUse, ToolUse: p}
	case "meteringEvent":
		return Event{Kind: EventMetering}
	case "contextUsageEvent":
		var p contextUsageEvent
		_ = json.Unmarshal(frame.Payload, &p)
		return Event{Kind: EventContextUsage, ContextUsagePercentage: p.ContextUsagePercentage}
	default:
		return Event{Kind: EventUnknown}
	}
}
