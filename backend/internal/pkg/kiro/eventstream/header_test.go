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
