package eventstream

import (
	"encoding/binary"
	"fmt"
)

// HeaderValueType 是 AWS Event Stream 协议定义的 10 种 header 值类型。
type HeaderValueType uint8

const (
	HeaderBoolTrue  HeaderValueType = 0
	HeaderBoolFalse HeaderValueType = 1
	HeaderByte      HeaderValueType = 2
	HeaderShort     HeaderValueType = 3
	HeaderInteger   HeaderValueType = 4
	HeaderLong      HeaderValueType = 5
	HeaderByteArray HeaderValueType = 6
	HeaderString    HeaderValueType = 7
	HeaderTimestamp HeaderValueType = 8
	HeaderUUID      HeaderValueType = 9
)

// HeaderValue 持有一个已解码的 header 值(tagged union),
// 由 Type 字段决定哪个数据字段有效。
type HeaderValue struct {
	Type  HeaderValueType
	Bool  bool     // BoolTrue / BoolFalse
	Int   int64    // Byte / Short / Integer / Long / Timestamp(毫秒)
	Bytes []byte   // ByteArray
	Str   string   // String
	UUID  [16]byte // Uuid
}

// AsString 返回字符串值;仅当类型为 String 时 ok 为 true。
func (v HeaderValue) AsString() (string, bool) {
	if v.Type == HeaderString {
		return v.Str, true
	}
	return "", false
}

// Headers 是一条消息的 header 集合。
type Headers map[string]HeaderValue

// GetString 返回指定 header 的字符串值。
func (h Headers) GetString(name string) (string, bool) {
	v, ok := h[name]
	if !ok {
		return "", false
	}
	return v.AsString()
}

// 以下为 AWS Event Stream 标准 header 的快捷访问器。
func (h Headers) MessageType() string   { s, _ := h.GetString(":message-type"); return s }
func (h Headers) EventType() string     { s, _ := h.GetString(":event-type"); return s }
func (h Headers) ExceptionType() string { s, _ := h.GetString(":exception-type"); return s }
func (h Headers) ErrorCode() string     { s, _ := h.GetString(":error-code"); return s }
func (h Headers) ContentType() string   { s, _ := h.GetString(":content-type"); return s }

// parseHeaders 从 header 区块字节流解析出 Headers。
// data 应恰好覆盖 header 区块(长度等于帧头声明的 Header Length)。
func parseHeaders(data []byte) (Headers, error) {
	headers := make(Headers)
	offset := 0
	for offset < len(data) {
		// 头部名称长度(1 byte)
		nameLen := int(data[offset])
		offset++
		if nameLen == 0 {
			return nil, &HeaderParseError{Msg: "header name length is zero"}
		}
		if offset+nameLen > len(data) {
			return nil, &HeaderParseError{Msg: "header name exceeds buffer"}
		}
		name := string(data[offset : offset+nameLen])
		offset += nameLen

		// 值类型(1 byte)
		if offset >= len(data) {
			return nil, &HeaderParseError{Msg: "missing header value type"}
		}
		vt := HeaderValueType(data[offset])
		offset++

		value, consumed, err := parseHeaderValue(data[offset:], vt)
		if err != nil {
			return nil, err
		}
		offset += consumed
		headers[name] = value
	}
	return headers, nil
}

// parseHeaderValue 依据 value type 解析一个 header 值,返回消耗的字节数。
func parseHeaderValue(data []byte, vt HeaderValueType) (HeaderValue, int, error) {
	need := func(n int) error {
		if len(data) < n {
			return &HeaderParseError{Msg: fmt.Sprintf("header value needs %d bytes, have %d", n, len(data))}
		}
		return nil
	}

	switch vt {
	case HeaderBoolTrue:
		return HeaderValue{Type: vt, Bool: true}, 0, nil
	case HeaderBoolFalse:
		return HeaderValue{Type: vt, Bool: false}, 0, nil
	case HeaderByte:
		if err := need(1); err != nil {
			return HeaderValue{}, 0, err
		}
		return HeaderValue{Type: vt, Int: int64(int8(data[0]))}, 1, nil
	case HeaderShort:
		if err := need(2); err != nil {
			return HeaderValue{}, 0, err
		}
		return HeaderValue{Type: vt, Int: int64(int16(binary.BigEndian.Uint16(data)))}, 2, nil
	case HeaderInteger:
		if err := need(4); err != nil {
			return HeaderValue{}, 0, err
		}
		return HeaderValue{Type: vt, Int: int64(int32(binary.BigEndian.Uint32(data)))}, 4, nil
	case HeaderLong, HeaderTimestamp:
		if err := need(8); err != nil {
			return HeaderValue{}, 0, err
		}
		return HeaderValue{Type: vt, Int: int64(binary.BigEndian.Uint64(data))}, 8, nil
	case HeaderByteArray:
		if err := need(2); err != nil {
			return HeaderValue{}, 0, err
		}
		n := int(binary.BigEndian.Uint16(data))
		if err := need(2 + n); err != nil {
			return HeaderValue{}, 0, err
		}
		b := make([]byte, n)
		copy(b, data[2:2+n])
		return HeaderValue{Type: vt, Bytes: b}, 2 + n, nil
	case HeaderString:
		if err := need(2); err != nil {
			return HeaderValue{}, 0, err
		}
		n := int(binary.BigEndian.Uint16(data))
		if err := need(2 + n); err != nil {
			return HeaderValue{}, 0, err
		}
		return HeaderValue{Type: vt, Str: string(data[2 : 2+n])}, 2 + n, nil
	case HeaderUUID:
		if err := need(16); err != nil {
			return HeaderValue{}, 0, err
		}
		var u [16]byte
		copy(u[:], data[:16])
		return HeaderValue{Type: vt, UUID: u}, 16, nil
	default:
		return HeaderValue{}, 0, &InvalidHeaderTypeError{Type: uint8(vt)}
	}
}
