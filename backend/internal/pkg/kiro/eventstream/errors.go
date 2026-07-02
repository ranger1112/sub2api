package eventstream

import "fmt"

// 以下错误类型对应 AWS Event Stream 帧解析的各类失败场景。
// 它们均实现 error 接口,调用方可用 errors.As 做类型判定。

// CRCMismatchError 表示 prelude 或 message 的 CRC32 校验失败。
// Part 取值为 "prelude" 或 "message"。
type CRCMismatchError struct {
	Part     string
	Expected uint32
	Actual   uint32
}

func (e *CRCMismatchError) Error() string {
	return fmt.Sprintf("eventstream: %s CRC mismatch: expected 0x%08x, actual 0x%08x", e.Part, e.Expected, e.Actual)
}

// MessageSizeError 表示 Total Length 超出允许范围。
// TooBig 为 true 表示超过上限,否则表示低于最小值。
type MessageSizeError struct {
	Length uint32
	Bound  uint32
	TooBig bool
}

func (e *MessageSizeError) Error() string {
	if e.TooBig {
		return fmt.Sprintf("eventstream: message too large: %d bytes (max %d)", e.Length, e.Bound)
	}
	return fmt.Sprintf("eventstream: message too small: %d bytes (min %d)", e.Length, e.Bound)
}

// InvalidHeaderTypeError 表示遇到未知的 header value type(合法取值 0..=9)。
type InvalidHeaderTypeError struct {
	Type uint8
}

func (e *InvalidHeaderTypeError) Error() string {
	return fmt.Sprintf("eventstream: invalid header value type: %d", e.Type)
}

// HeaderParseError 表示 header 区块解析失败(名称长度为 0、越界等)。
type HeaderParseError struct {
	Msg string
}

func (e *HeaderParseError) Error() string {
	return "eventstream: header parse failed: " + e.Msg
}

// TooManyErrorsError 表示解码器连续错误达到上限而停止。
type TooManyErrorsError struct {
	Count     int
	LastError string
}

func (e *TooManyErrorsError) Error() string {
	return fmt.Sprintf("eventstream: too many consecutive errors (%d), decoder stopped: %s", e.Count, e.LastError)
}

// BufferOverflowError 表示解码器内部缓冲超过上限。
type BufferOverflowError struct {
	Size int
	Max  int
}

func (e *BufferOverflowError) Error() string {
	return fmt.Sprintf("eventstream: buffer overflow: %d bytes (max %d)", e.Size, e.Max)
}

// IncompleteFrameError 表示流结束(EOF)时缓冲仍残留不足以构成完整帧的字节,
// 通常意味着上游响应被截断。
type IncompleteFrameError struct {
	Residual int
}

func (e *IncompleteFrameError) Error() string {
	return fmt.Sprintf("eventstream: incomplete trailing frame: %d residual bytes", e.Residual)
}
