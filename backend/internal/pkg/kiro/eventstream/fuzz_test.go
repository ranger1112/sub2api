package eventstream

import "testing"

// FuzzParseFrame 保证 parseFrame 面对任意(含截断/损坏)输入都不 panic,
// 且成功时返回的 consumed 落在合法区间内。种子会在普通 go test 下执行。
func FuzzParseFrame(f *testing.F) {
	f.Add([]byte(nil))
	f.Add([]byte{0, 0, 0, 16})
	f.Add([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
	f.Add(encodeFrame([]testHeader{{":message-type", "event"}}, []byte("hi")))

	f.Fuzz(func(t *testing.T, data []byte) {
		frame, consumed, err := parseFrame(data)
		if err == nil && frame != nil {
			if consumed < MinMessageSize || consumed > len(data) {
				t.Fatalf("consumed=%d out of range for len=%d", consumed, len(data))
			}
		}
	})
}

// FuzzDecoder 保证流式解码器面对任意输入既不 panic 也不死循环。
func FuzzDecoder(f *testing.F) {
	f.Add([]byte("garbage-bytes"))
	f.Add(encodeFrame([]testHeader{{":message-type", "event"}}, []byte("x")))
	f.Add(corruptStream(3))

	f.Fuzz(func(t *testing.T, data []byte) {
		d := NewDecoder()
		if err := d.Feed(data); err != nil {
			return
		}
		_, _ = d.DecodeAvailable()
		_ = d.Finish()
	})
}
