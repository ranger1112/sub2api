package eventstream

import (
	"errors"
	"testing"
)

// corruptStream 拼接 n 个 prelude 完好但 message CRC 损坏的帧,用于驱动连续错误。
func corruptStream(n int) []byte {
	var stream []byte
	for i := 0; i < n; i++ {
		f := encodeFrame([]testHeader{{":message-type", "event"}}, []byte("bad"))
		f[len(f)-1] ^= 0xFF
		stream = append(stream, f...)
	}
	return stream
}

func TestDecoder_SingleFrameChunked(t *testing.T) {
	buf := encodeFrame([]testHeader{
		{":message-type", "event"},
		{":event-type", "assistantResponseEvent"},
	}, []byte(`{"content":"hi"}`))

	d := NewDecoder()

	// 先喂一半:应当数据不足。
	mid := len(buf) / 2
	if err := d.Feed(buf[:mid]); err != nil {
		t.Fatalf("Feed error: %v", err)
	}
	if frame, err := d.Decode(); frame != nil || err != nil {
		t.Fatalf("half-fed Decode = (%v, %v), want (nil, nil)", frame, err)
	}

	// 喂入剩余部分:应当解出完整帧。
	if err := d.Feed(buf[mid:]); err != nil {
		t.Fatalf("Feed error: %v", err)
	}
	frame, err := d.Decode()
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	if frame == nil {
		t.Fatal("expected a frame after full feed")
	}
	if frame.EventType() != "assistantResponseEvent" || string(frame.Payload) != `{"content":"hi"}` {
		t.Fatalf("unexpected frame: type=%q payload=%q", frame.EventType(), frame.Payload)
	}
}

func TestDecoder_MultipleFramesOneBuffer(t *testing.T) {
	var stream []byte
	for _, txt := range []string{"a", "b", "c"} {
		stream = append(stream, encodeFrame(
			[]testHeader{{":message-type", "event"}, {":event-type", "chunk"}},
			[]byte(txt),
		)...)
	}

	d := NewDecoder()
	if err := d.Feed(stream); err != nil {
		t.Fatalf("Feed error: %v", err)
	}
	frames, err := d.DecodeAvailable()
	if err != nil {
		t.Fatalf("DecodeAvailable error: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("got %d frames, want 3", len(frames))
	}
	for i, want := range []string{"a", "b", "c"} {
		if string(frames[i].Payload) != want {
			t.Fatalf("frame[%d] payload = %q, want %q", i, frames[i].Payload, want)
		}
	}
	if d.FramesDecoded() != 3 {
		t.Fatalf("FramesDecoded = %d, want 3", d.FramesDecoded())
	}
}

func TestDecoder_RecoversFromCorruptFrame(t *testing.T) {
	good1 := encodeFrame([]testHeader{{":message-type", "event"}}, []byte("first"))
	corrupt := encodeFrame([]testHeader{{":message-type", "event"}}, []byte("BROKEN"))
	corrupt[len(corrupt)-1] ^= 0xFF // 破坏 message CRC,但 prelude / Total Length 仍完好
	good2 := encodeFrame([]testHeader{{":message-type", "event"}}, []byte("second"))

	var stream []byte
	stream = append(stream, good1...)
	stream = append(stream, corrupt...)
	stream = append(stream, good2...)

	d := NewDecoder()
	if err := d.Feed(stream); err != nil {
		t.Fatalf("Feed error: %v", err)
	}
	frames, err := d.DecodeAvailable()
	if err != nil {
		t.Fatalf("DecodeAvailable error: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("got %d frames, want 2 (corrupt frame skipped)", len(frames))
	}
	if string(frames[0].Payload) != "first" || string(frames[1].Payload) != "second" {
		t.Fatalf("payloads = %q,%q want first,second", frames[0].Payload, frames[1].Payload)
	}
	if d.BytesSkipped() != len(corrupt) {
		t.Fatalf("BytesSkipped = %d, want %d (whole corrupt frame)", d.BytesSkipped(), len(corrupt))
	}
}

func TestDecoder_BufferOverflow(t *testing.T) {
	d := NewDecoder()
	d.maxBufferSize = 8
	if err := d.Feed([]byte("123456789")); err == nil {
		t.Fatal("expected BufferOverflowError")
	} else if _, ok := err.(*BufferOverflowError); !ok {
		t.Fatalf("err = %v, want *BufferOverflowError", err)
	}
}

func TestDecoder_MaxErrorsStops(t *testing.T) {
	d := NewDecoder()
	if err := d.Feed(corruptStream(DefaultMaxErrors)); err != nil {
		t.Fatalf("Feed error: %v", err)
	}
	frames, err := d.DecodeAvailable()
	var tme *TooManyErrorsError
	if !errors.As(err, &tme) {
		t.Fatalf("err = %v, want TooManyErrorsError", err)
	}
	if len(frames) != 0 {
		t.Fatalf("got %d frames, want 0", len(frames))
	}
}

func TestDecoder_StoppedThenFeedStaysStopped(t *testing.T) {
	d := NewDecoder()
	_ = d.Feed(corruptStream(DefaultMaxErrors))
	_, _ = d.DecodeAvailable() // 现在应处于 Stopped

	good := encodeFrame([]testHeader{{":message-type", "event"}}, []byte("ok"))
	if err := d.Feed(good); err != nil {
		t.Fatalf("Feed after stop: %v", err)
	}
	frame, err := d.Decode()
	var tme *TooManyErrorsError
	if frame != nil || !errors.As(err, &tme) {
		t.Fatalf("got (%v, %v), want (nil, TooManyErrorsError)", frame, err)
	}
}

func TestDecoder_ResetRecoversFromStopped(t *testing.T) {
	d := NewDecoder()
	_ = d.Feed(corruptStream(DefaultMaxErrors))
	_, _ = d.DecodeAvailable() // Stopped

	d.Reset()
	good := encodeFrame([]testHeader{{":message-type", "event"}}, []byte("ok"))
	if err := d.Feed(good); err != nil {
		t.Fatalf("Feed after reset: %v", err)
	}
	frame, err := d.Decode()
	if err != nil || frame == nil || string(frame.Payload) != "ok" {
		t.Fatalf("after reset got (%v, %v), want frame payload=ok", frame, err)
	}
	if d.FramesDecoded() != 1 {
		t.Fatalf("FramesDecoded = %d, want 1 (reset should have zeroed counters)", d.FramesDecoded())
	}
}

func TestDecoder_FinishDetectsTruncation(t *testing.T) {
	full := encodeFrame([]testHeader{{":message-type", "event"}}, []byte("hi"))

	d := NewDecoder()
	_ = d.Feed(full[:len(full)-3]) // 只喂入部分帧
	if _, err := d.DecodeAvailable(); err != nil {
		t.Fatalf("DecodeAvailable(partial) = %v, want nil (need more data)", err)
	}
	err := d.Finish()
	var inc *IncompleteFrameError
	if !errors.As(err, &inc) {
		t.Fatalf("Finish(truncated) = %v, want IncompleteFrameError", err)
	}

	// 完整流结束后不应有残留。
	d.Reset()
	_ = d.Feed(full)
	frames, _ := d.DecodeAvailable()
	if len(frames) != 1 {
		t.Fatalf("got %d frames, want 1", len(frames))
	}
	if err := d.Finish(); err != nil {
		t.Fatalf("Finish(complete) = %v, want nil", err)
	}
}
