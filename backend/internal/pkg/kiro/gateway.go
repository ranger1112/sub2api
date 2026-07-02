package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/kiro/eventstream"
)

// UpstreamError 表示 Kiro 上游返回了非 2xx 响应。
type UpstreamError struct {
	StatusCode int
	Body       string
}

func (e *UpstreamError) Error() string {
	return fmt.Sprintf("kiro: upstream returned HTTP %d: %s", e.StatusCode, truncateForError(e.Body))
}

// StreamResult 汇总一次流式请求的结果,供上层计费 / 观测使用。
type StreamResult struct {
	Model        string
	InputTokens  int
	OutputTokens int
}

// maxUpstreamErrorBody 是读取错误响应体时的上限。
const maxUpstreamErrorBody = 64 * 1024

// StreamMessages 执行一次 Anthropic /v1/messages → Kiro 请求,并把转换后的 Anthropic SSE 写入 w。
//
// 它把整条链路串起来:Convert → 序列化 ConversationState → BuildAPIRequest(注入 profileArn)
// → client.Do → eventstream 流式解码 → StreamContext → 写 SSE。
//
// client 由调用方提供(可携带代理 / TLS 指纹)。cred 必须是有效(未过期)凭据;token 刷新
// 由调用方负责。若 w 实现了 Flush(),每批事件写入后会自动 flush,以支持实时流。
func StreamMessages(ctx context.Context, client *http.Client, cred *Credentials, cfg ClientConfig, req *AnthropicRequest, w io.Writer) (*StreamResult, error) {
	return runStream(ctx, client, cred, cfg, req, func(events []SSEEvent) error {
		return writeEvents(w, events)
	})
}

// CollectMessages 执行一次请求但不流式输出,而是把全部事件收集后拼装成一条完整的
// Anthropic Messages 响应(用于 stream=false 的 /v1/messages)。返回拼装后的消息 JSON 对象、
// 计费结果与错误。
func CollectMessages(ctx context.Context, client *http.Client, cred *Credentials, cfg ClientConfig, req *AnthropicRequest) (map[string]any, *StreamResult, error) {
	var collected []SSEEvent
	result, err := runStream(ctx, client, cred, cfg, req, func(events []SSEEvent) error {
		collected = append(collected, events...)
		return nil
	})
	if err != nil {
		return nil, result, err
	}
	return AssembleMessage(collected), result, nil
}

// runStream 是 StreamMessages / CollectMessages 共享的核心:每产生一批 SSE 事件即回调 sink。
// sink 返回错误会立即中止(用于写入失败等场景)。
func runStream(ctx context.Context, client *http.Client, cred *Credentials, cfg ClientConfig, req *AnthropicRequest, sink func([]SSEEvent) error) (*StreamResult, error) {
	conv, err := Convert(req)
	if err != nil {
		return nil, err
	}

	body, err := json.Marshal(&KiroRequest{ConversationState: conv.ConversationState})
	if err != nil {
		return nil, err
	}

	httpReq, err := BuildAPIRequest(ctx, cred, cfg, body)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxUpstreamErrorBody))
		return nil, &UpstreamError{StatusCode: resp.StatusCode, Body: string(errBody)}
	}

	sc := NewStreamContext(conv.ModelID, estimateInputTokens(req), conv.Thinking, conv.ToolNameMap)
	if err := sink(sc.GenerateInitialEvents()); err != nil {
		return nil, err
	}

	dec := eventstream.NewDecoder()
	buf := make([]byte, 16*1024)
	var streamErr error

	for streamErr == nil {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if feedErr := dec.Feed(buf[:n]); feedErr != nil {
				streamErr = feedErr
				break
			}
			frames, decErr := dec.DecodeAvailable()
			for _, f := range frames {
				if err := sink(sc.ProcessKiroEvent(EventFromFrame(f))); err != nil {
					return nil, err
				}
			}
			if decErr != nil {
				streamErr = decErr
				break
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			streamErr = readErr
			break
		}
	}

	// 干净 EOF 后检测截断:若解码器仍残留不足以构成完整帧的字节,说明流被截断,
	// 应作为错误上报,而非静默地当作成功。
	if streamErr == nil {
		if err := dec.Finish(); err != nil {
			streamErr = err
		}
	}

	// 收尾事件始终发送(即便中途出错,也让客户端得到一个可闭合的消息)。
	finalEvents := sc.GenerateFinalEvents()
	result := &StreamResult{
		Model:        conv.ModelID,
		InputTokens:  sc.FinalInputTokens(),
		OutputTokens: sc.OutputTokens,
	}
	// 终写失败不丢弃已算出的 result 或先前的 streamErr。
	if err := sink(finalEvents); err != nil && streamErr == nil {
		streamErr = err
	}
	return result, streamErr
}

// writeEvents 把一批 SSE 事件写入 w,并在可能时 flush。
func writeEvents(w io.Writer, events []SSEEvent) error {
	if len(events) == 0 {
		return nil
	}
	for _, e := range events {
		if _, err := io.WriteString(w, e.String()); err != nil {
			return err
		}
	}
	flushWriter(w)
	return nil
}

// flushWriter 支持 http.Flusher(Flush())与 *bufio.Writer(Flush() error)两种写入器。
func flushWriter(w io.Writer) {
	switch f := w.(type) {
	case interface{ Flush() }:
		f.Flush()
	case interface{ Flush() error }:
		_ = f.Flush()
	}
}

// estimateInputTokens 从请求粗略估算 input tokens(contextUsageEvent 到达后会被更准确的值覆盖)。
func estimateInputTokens(req *AnthropicRequest) int {
	total := estimateTokens(parseSystemText(req.System))
	for _, m := range req.Messages {
		text, _, _, _ := processMessageContent(m.Content)
		total += estimateTokens(text)
	}
	if total < 1 {
		return 1
	}
	return total
}

func truncateForError(s string) string {
	const max = 512
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
