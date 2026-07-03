package eventstream

import (
	"encoding/binary"
	"hash/crc32"
)

const (
	// PreludeSize 是 prelude 固定大小(Total Length + Header Length + Prelude CRC)。
	PreludeSize = 12
	// MinMessageSize 是最小合法消息大小(prelude + message CRC)。
	MinMessageSize = PreludeSize + 4
	// MaxMessageSize 是单条消息的上限(16 MB)。
	MaxMessageSize = 16 * 1024 * 1024
)

// crc32IEEE 计算 CRC-32 / ISO-HDLC(多项式 0xEDB88320),与 AWS Event Stream 一致。
func crc32IEEE(data []byte) uint32 {
	return crc32.ChecksumIEEE(data)
}

// Frame 是一条已解析的消息帧。
type Frame struct {
	Headers Headers
	Payload []byte
}

// MessageType 返回 :message-type header(如 "event" / "exception")。
func (f *Frame) MessageType() string { return f.Headers.MessageType() }

// EventType 返回 :event-type header(如 "assistantResponseEvent")。
func (f *Frame) EventType() string { return f.Headers.EventType() }

// parseFrame 尝试从 buffer 头部解析出一个完整帧,是无状态纯函数。
//
// 返回值约定:
//   - frame != nil:成功,consumed 为该帧消耗的字节数
//   - frame == nil 且 err == nil:数据不足,需要更多字节
//   - err != nil:解析错误(CRC 失败、长度异常、header 损坏等)
func parseFrame(buffer []byte) (frame *Frame, consumed int, err error) {
	if len(buffer) < PreludeSize {
		return nil, 0, nil
	}

	totalLength := binary.BigEndian.Uint32(buffer[0:4])
	headerLength := binary.BigEndian.Uint32(buffer[4:8])
	preludeCRC := binary.BigEndian.Uint32(buffer[8:12])

	// 先用 Prelude CRC 认证前 8 字节(Total/Header Length),再据其做长度判断与等待。
	// 这样可避免被损坏的 Total Length 误导——例如等待一段永不到达的数据,或把后续
	// 合法帧误纳入一个虚高的消息窗口而放大内存/延迟。
	if actual := crc32IEEE(buffer[:8]); actual != preludeCRC {
		return nil, 0, &CRCMismatchError{Part: "prelude", Expected: preludeCRC, Actual: actual}
	}

	if totalLength < MinMessageSize {
		return nil, 0, &MessageSizeError{Length: totalLength, Bound: MinMessageSize, TooBig: false}
	}
	if totalLength > MaxMessageSize {
		return nil, 0, &MessageSizeError{Length: totalLength, Bound: MaxMessageSize, TooBig: true}
	}

	total := int(totalLength)
	hdrLen := int(headerLength)

	// 消息尚未完整到达,等待更多数据(Total Length 已通过 Prelude CRC 认证)。
	if len(buffer) < total {
		return nil, 0, nil
	}

	// 校验 Message CRC(整条消息去除末尾 4 字节)。
	messageCRC := binary.BigEndian.Uint32(buffer[total-4 : total])
	if actual := crc32IEEE(buffer[:total-4]); actual != messageCRC {
		return nil, 0, &CRCMismatchError{Part: "message", Expected: messageCRC, Actual: actual}
	}

	headersStart := PreludeSize
	headersEnd := headersStart + hdrLen
	// headersEnd < headersStart 防护 32-bit 平台上 int(headerLength) 溢出为负导致越界。
	if headersEnd < headersStart || headersEnd > total-4 {
		return nil, 0, &HeaderParseError{Msg: "header length exceeds message boundary"}
	}

	headers, err := parseHeaders(buffer[headersStart:headersEnd])
	if err != nil {
		return nil, 0, err
	}

	// payload 为 headers 之后、message CRC 之前的部分,拷贝一份以脱离底层缓冲。
	payload := make([]byte, total-4-headersEnd)
	copy(payload, buffer[headersEnd:total-4])

	return &Frame{Headers: headers, Payload: payload}, total, nil
}
