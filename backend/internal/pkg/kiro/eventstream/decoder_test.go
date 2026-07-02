package eventstream

import (
	"testing"
)

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
