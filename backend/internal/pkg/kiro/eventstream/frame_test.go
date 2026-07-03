package eventstream

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"testing"
)

// testHeader 是构造测试帧用的 string 类型 header。
type testHeader struct {
	name  string
	value string
}

// encodeFrame 构造一条合法的 AWS Event Stream 帧(header 均为 String 类型),
// 供解码器测试作为已知正确输入。CRC 用标准库独立计算,与被测代码解耦。
func encodeFrame(headers []testHeader, payload []byte) []byte {
	var hb []byte
	for _, h := range headers {
		hb = append(hb, byte(len(h.name)))
		hb = append(hb, h.name...)
		hb = append(hb, byte(HeaderString))
		var lb [2]byte
		binary.BigEndian.PutUint16(lb[:], uint16(len(h.value)))
		hb = append(hb, lb[:]...)
		hb = append(hb, h.value...)
	}

	total := PreludeSize + len(hb) + len(payload) + 4
	buf := make([]byte, total)
	binary.BigEndian.PutUint32(buf[0:4], uint32(total))
	binary.BigEndian.PutUint32(buf[4:8], uint32(len(hb)))
	binary.BigEndian.PutUint32(buf[8:12], crc32.ChecksumIEEE(buf[0:8]))
	copy(buf[PreludeSize:], hb)
	copy(buf[PreludeSize+len(hb):], payload)
	binary.BigEndian.PutUint32(buf[total-4:], crc32.ChecksumIEEE(buf[0:total-4]))
	return buf
}

func TestCRC32IEEE_KnownAnswers(t *testing.T) {
	if got := crc32IEEE(nil); got != 0 {
		t.Fatalf("crc32 of empty = 0x%08x, want 0", got)
	}
	// "123456789" 的 CRC-32/ISO-HDLC 标准检验值。
	if got := crc32IEEE([]byte("123456789")); got != 0xCBF43926 {
		t.Fatalf("crc32(\"123456789\") = 0x%08x, want 0xCBF43926", got)
	}
}

func TestParseFrame_RoundTrip(t *testing.T) {
	payload := []byte(`{"content":"hello"}`)
	buf := encodeFrame([]testHeader{
		{":message-type", "event"},
		{":event-type", "assistantResponseEvent"},
		{":content-type", "application/json"},
	}, payload)

	frame, consumed, err := parseFrame(buf)
	if err != nil {
		t.Fatalf("parseFrame error: %v", err)
	}
	if frame == nil {
		t.Fatal("expected frame, got nil (need more data)")
	}
	if consumed != len(buf) {
		t.Fatalf("consumed = %d, want %d", consumed, len(buf))
	}
	if frame.MessageType() != "event" {
		t.Fatalf("MessageType = %q, want event", frame.MessageType())
	}
	if frame.EventType() != "assistantResponseEvent" {
		t.Fatalf("EventType = %q, want assistantResponseEvent", frame.EventType())
	}
	if string(frame.Payload) != string(payload) {
		t.Fatalf("Payload = %q, want %q", frame.Payload, payload)
	}
}

func TestParseFrame_InsufficientData(t *testing.T) {
	// 少于 prelude 大小 → 需要更多数据(非错误)。
	frame, consumed, err := parseFrame([]byte{0, 0, 0})
	if err != nil || frame != nil || consumed != 0 {
		t.Fatalf("got (frame=%v, consumed=%d, err=%v), want (nil, 0, nil)", frame, consumed, err)
	}

	// prelude 完整但消息未收全 → 需要更多数据。
	full := encodeFrame([]testHeader{{":message-type", "event"}}, []byte("x"))
	frame, consumed, err = parseFrame(full[:len(full)-2])
	if err != nil || frame != nil || consumed != 0 {
		t.Fatalf("partial: got (frame=%v, consumed=%d, err=%v), want (nil, 0, nil)", frame, consumed, err)
	}
}

func TestParseFrame_MessageTooSmall(t *testing.T) {
	buf := make([]byte, 16)
	binary.BigEndian.PutUint32(buf[0:4], 10) // < MinMessageSize
	binary.BigEndian.PutUint32(buf[4:8], 0)
	binary.BigEndian.PutUint32(buf[8:12], crc32.ChecksumIEEE(buf[0:8]))

	_, _, err := parseFrame(buf)
	var sizeErr *MessageSizeError
	if !errors.As(err, &sizeErr) || sizeErr.TooBig {
		t.Fatalf("err = %v, want MessageSizeError(too small)", err)
	}
}

func TestParseFrame_PreludeCRCMismatch(t *testing.T) {
	buf := encodeFrame([]testHeader{{":message-type", "event"}}, []byte("x"))
	buf[8] ^= 0xFF // 破坏 prelude CRC

	_, _, err := parseFrame(buf)
	var crcErr *CRCMismatchError
	if !errors.As(err, &crcErr) || crcErr.Part != "prelude" {
		t.Fatalf("err = %v, want prelude CRCMismatchError", err)
	}
}

func TestParseFrame_MessageCRCMismatch(t *testing.T) {
	buf := encodeFrame([]testHeader{{":message-type", "event"}}, []byte("hello"))
	buf[len(buf)-1] ^= 0xFF // 破坏 message CRC(prelude 仍合法)

	_, _, err := parseFrame(buf)
	var crcErr *CRCMismatchError
	if !errors.As(err, &crcErr) || crcErr.Part != "message" {
		t.Fatalf("err = %v, want message CRCMismatchError", err)
	}
}

func TestParseFrame_ZeroHeadersAndPayload(t *testing.T) {
	// 无 header、无 payload —— 最小合法帧(恰为 MinMessageSize)。
	buf := encodeFrame(nil, nil)
	if len(buf) != MinMessageSize {
		t.Fatalf("empty frame size = %d, want %d", len(buf), MinMessageSize)
	}
	frame, consumed, err := parseFrame(buf)
	if err != nil || frame == nil || consumed != len(buf) {
		t.Fatalf("got (frame=%v, consumed=%d, err=%v)", frame, consumed, err)
	}
	if len(frame.Headers) != 0 || len(frame.Payload) != 0 {
		t.Fatalf("headers=%d payload=%d, want 0/0", len(frame.Headers), len(frame.Payload))
	}
	if frame.MessageType() != "" {
		t.Fatalf("MessageType = %q, want empty", frame.MessageType())
	}
}

func TestParseFrame_ZeroPayloadWithHeaders(t *testing.T) {
	buf := encodeFrame([]testHeader{{":message-type", "event"}}, nil)
	frame, _, err := parseFrame(buf)
	if err != nil || frame == nil {
		t.Fatalf("got (frame=%v, err=%v)", frame, err)
	}
	if len(frame.Payload) != 0 {
		t.Fatalf("payload len = %d, want 0", len(frame.Payload))
	}
	if frame.MessageType() != "event" {
		t.Fatalf("MessageType = %q, want event", frame.MessageType())
	}
}

func TestParseFrame_MessageTooLarge(t *testing.T) {
	buf := make([]byte, PreludeSize)
	binary.BigEndian.PutUint32(buf[0:4], uint32(MaxMessageSize)+1)
	binary.BigEndian.PutUint32(buf[4:8], 0)
	binary.BigEndian.PutUint32(buf[8:12], crc32.ChecksumIEEE(buf[0:8]))

	_, _, err := parseFrame(buf)
	var sizeErr *MessageSizeError
	if !errors.As(err, &sizeErr) || !sizeErr.TooBig {
		t.Fatalf("err = %v, want MessageSizeError(too big)", err)
	}
}

func TestParseFrame_HeaderLengthExceedsBoundary(t *testing.T) {
	// Total Length 与 Header Length 均通过 CRC,但 Header Length 越出帧边界:应报 HeaderParseError。
	const total = 20
	buf := make([]byte, total)
	binary.BigEndian.PutUint32(buf[0:4], uint32(total))
	binary.BigEndian.PutUint32(buf[4:8], 100) // 远超帧本身
	binary.BigEndian.PutUint32(buf[8:12], crc32.ChecksumIEEE(buf[0:8]))
	binary.BigEndian.PutUint32(buf[total-4:], crc32.ChecksumIEEE(buf[0:total-4]))

	_, _, err := parseFrame(buf)
	var hpErr *HeaderParseError
	if !errors.As(err, &hpErr) {
		t.Fatalf("err = %v, want HeaderParseError (boundary)", err)
	}
}
