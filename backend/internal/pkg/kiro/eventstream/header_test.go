package eventstream

import (
	"encoding/binary"
	"errors"
	"testing"
)

func TestParseHeaders_KnownStringBytes(t *testing.T) {
	// 取自 kiro.rs 参考测试的已知字节:
	// name_len(1) + "x" + type(7=String) + value_len(2) + "ab"
	data := []byte{1, 'x', 7, 0, 2, 'a', 'b'}
	headers, err := parseHeaders(data)
	if err != nil {
		t.Fatalf("parseHeaders error: %v", err)
	}
	if got, ok := headers.GetString("x"); !ok || got != "ab" {
		t.Fatalf("headers[x] = (%q, %v), want (\"ab\", true)", got, ok)
	}
}

func TestParseHeaders_AllScalarTypes(t *testing.T) {
	var data []byte
	appendHeader := func(name string, vt HeaderValueType, val []byte) {
		data = append(data, byte(len(name)))
		data = append(data, name...)
		data = append(data, byte(vt))
		data = append(data, val...)
	}

	appendHeader("bt", HeaderBoolTrue, nil)
	appendHeader("bf", HeaderBoolFalse, nil)
	appendHeader("by", HeaderByte, []byte{0xFF}) // int8(-1)
	var s16 int16 = -2
	shortV := make([]byte, 2)
	binary.BigEndian.PutUint16(shortV, uint16(s16))
	appendHeader("sh", HeaderShort, shortV)
	var i32 int32 = -3
	intV := make([]byte, 4)
	binary.BigEndian.PutUint32(intV, uint32(i32))
	appendHeader("in", HeaderInteger, intV)
	var i64 int64 = -4
	longV := make([]byte, 8)
	binary.BigEndian.PutUint64(longV, uint64(i64))
	appendHeader("lo", HeaderLong, longV)

	headers, err := parseHeaders(data)
	if err != nil {
		t.Fatalf("parseHeaders error: %v", err)
	}
	if v := headers["bt"]; !v.Bool || v.Type != HeaderBoolTrue {
		t.Fatalf("bt = %+v", v)
	}
	if v := headers["bf"]; v.Bool || v.Type != HeaderBoolFalse {
		t.Fatalf("bf = %+v", v)
	}
	for name, want := range map[string]int64{"by": -1, "sh": -2, "in": -3, "lo": -4} {
		if got := headers[name].Int; got != want {
			t.Fatalf("%s = %d, want %d", name, got, want)
		}
	}
}

func TestParseHeaders_InvalidType(t *testing.T) {
	data := []byte{1, 'x', 99} // type 99 非法
	_, err := parseHeaders(data)
	var typeErr *InvalidHeaderTypeError
	if !errors.As(err, &typeErr) || typeErr.Type != 99 {
		t.Fatalf("err = %v, want InvalidHeaderTypeError{99}", err)
	}
}

func TestParseHeaders_ZeroNameLength(t *testing.T) {
	_, err := parseHeaders([]byte{0})
	var hpErr *HeaderParseError
	if !errors.As(err, &hpErr) {
		t.Fatalf("err = %v, want HeaderParseError", err)
	}
}

func TestParseHeaders_ByteArrayTimestampUUID(t *testing.T) {
	var data []byte
	appendHeader := func(name string, vt HeaderValueType, val []byte) {
		data = append(data, byte(len(name)))
		data = append(data, name...)
		data = append(data, byte(vt))
		data = append(data, val...)
	}
	appendHeader("ba", HeaderByteArray, []byte{0x00, 0x03, 0xAA, 0xBB, 0xCC}) // 2-byte len prefix + 3 bytes
	ts := make([]byte, 8)
	binary.BigEndian.PutUint64(ts, 1_700_000_000_000)
	appendHeader("ts", HeaderTimestamp, ts)
	uu := make([]byte, 16)
	for i := range uu {
		uu[i] = byte(i)
	}
	appendHeader("id", HeaderUUID, uu)

	headers, err := parseHeaders(data)
	if err != nil {
		t.Fatalf("parseHeaders error: %v", err)
	}
	if got := headers["ba"]; got.Type != HeaderByteArray || len(got.Bytes) != 3 || got.Bytes[0] != 0xAA || got.Bytes[2] != 0xCC {
		t.Fatalf("ba = %+v", got)
	}
	if got := headers["ts"]; got.Type != HeaderTimestamp || got.Int != 1_700_000_000_000 {
		t.Fatalf("ts = %+v", got)
	}
	if got := headers["id"]; got.Type != HeaderUUID || got.UUID[0] != 0 || got.UUID[15] != 15 {
		t.Fatalf("id = %+v", got)
	}
}

func TestParseHeaders_ValueLengthExceedsBlock(t *testing.T) {
	// String header 声明长度 200,但 header 区块只有几字节:必须报错,不得越界读入 payload,也不得 panic。
	data := []byte{1, 'x', 7, 0, 200, 'a', 'b'}
	_, err := parseHeaders(data)
	var hpErr *HeaderParseError
	if !errors.As(err, &hpErr) {
		t.Fatalf("err = %v, want HeaderParseError", err)
	}
}
