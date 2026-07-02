package eventstream

import "encoding/binary"

// decoderState 是流式解码器的四态状态机(参考 kiro.rs 的设计):
// Ready(就绪)→ Parsing(解析中)→ Ready/Recovering(恢复中)→ 直至 Stopped(停止)。
type decoderState int

const (
	stateReady decoderState = iota
	stateParsing
	stateRecovering
	stateStopped
)

const (
	// DefaultMaxBufferSize 是内部缓冲的默认上限(16 MB)。
	DefaultMaxBufferSize = 16 * 1024 * 1024
	// DefaultMaxErrors 是触发停止前允许的最大连续错误数。
	DefaultMaxErrors = 5
)

// Decoder 是一个有状态的流式帧解码器。它把陆续到达的字节块累积到内部缓冲,
// 并在数据足够时逐帧解析。Decoder 非并发安全,应由单个 goroutine 使用。
type Decoder struct {
	buf           []byte
	state         decoderState
	framesDecoded int
	errorCount    int
	maxErrors     int
	maxBufferSize int
	bytesSkipped  int
}

// NewDecoder 创建一个使用默认参数的解码器。
func NewDecoder() *Decoder {
	return &Decoder{
		state:         stateReady,
		maxErrors:     DefaultMaxErrors,
		maxBufferSize: DefaultMaxBufferSize,
	}
}

// Feed 向解码器追加一段数据。若累计缓冲超过上限,返回 *BufferOverflowError。
func (d *Decoder) Feed(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	if len(d.buf)+len(data) > d.maxBufferSize {
		return &BufferOverflowError{Size: len(d.buf) + len(data), Max: d.maxBufferSize}
	}
	d.buf = append(d.buf, data...)
	if d.state == stateRecovering {
		d.state = stateReady
	}
	return nil
}

// Decode 尝试解析下一个帧。
//   - (frame, nil):成功解析一个帧
//   - (nil, nil):数据不足,需要继续 Feed
//   - (nil, err):解析错误;若为可恢复错误,内部已跳过损坏数据并进入 Recovering
func (d *Decoder) Decode() (*Frame, error) {
	if d.state == stateStopped {
		return nil, &TooManyErrorsError{Count: d.errorCount, LastError: "decoder stopped"}
	}
	if len(d.buf) == 0 {
		d.state = stateReady
		return nil, nil
	}
	d.state = stateParsing

	frame, consumed, err := parseFrame(d.buf)
	switch {
	case err == nil && frame != nil:
		d.consume(consumed)
		d.state = stateReady
		d.framesDecoded++
		d.errorCount = 0
		return frame, nil
	case err == nil && frame == nil:
		// 数据不足,等待更多字节。
		d.state = stateReady
		return nil, nil
	default:
		d.errorCount++
		if d.errorCount >= d.maxErrors {
			d.state = stateStopped
			return nil, &TooManyErrorsError{Count: d.errorCount, LastError: err.Error()}
		}
		d.tryRecover(err)
		d.state = stateRecovering
		return nil, err
	}
}

// DecodeAvailable 解析当前缓冲中所有已完整到达的帧,遇到数据不足即返回。
// 遇到可恢复错误时会跳过损坏数据并继续;仅当解码器因连续错误停止时返回错误。
func (d *Decoder) DecodeAvailable() ([]*Frame, error) {
	var frames []*Frame
	for {
		frame, err := d.Decode()
		if err != nil {
			if d.state == stateStopped {
				return frames, err
			}
			// 可恢复错误:tryRecover 已跳过损坏字节,回到 Ready 继续尝试。
			d.state = stateReady
			continue
		}
		if frame == nil {
			return frames, nil
		}
		frames = append(frames, frame)
	}
}

// FramesDecoded 返回已成功解码的帧总数(用于观测/调试)。
func (d *Decoder) FramesDecoded() int { return d.framesDecoded }

// BytesSkipped 返回容错恢复过程中跳过的字节总数(用于观测/调试)。
func (d *Decoder) BytesSkipped() int { return d.bytesSkipped }

// consume 丢弃缓冲前 n 字节,并把剩余数据移动到底层数组头部,以在长连接下限制内存占用。
func (d *Decoder) consume(n int) {
	rem := copy(d.buf, d.buf[n:])
	d.buf = d.buf[:rem]
}

// tryRecover 根据错误类型选择恢复策略:
//   - prelude 阶段错误(prelude CRC、长度异常):帧边界可能错位,逐字节前移;
//   - data 阶段错误(message CRC、header 损坏):帧边界正确,按 Total Length 跳过整帧,失败则退化为逐字节。
func (d *Decoder) tryRecover(err error) {
	if len(d.buf) == 0 {
		return
	}
	switch e := err.(type) {
	case *CRCMismatchError:
		if e.Part == "message" && d.skipWholeFrame() {
			return
		}
		d.skipByte()
	case *MessageSizeError:
		d.skipByte()
	case *HeaderParseError:
		if d.skipWholeFrame() {
			return
		}
		d.skipByte()
	default:
		d.skipByte()
	}
}

// skipWholeFrame 依据 prelude 中的 Total Length 跳过一整帧,长度不合理时返回 false。
func (d *Decoder) skipWholeFrame() bool {
	if len(d.buf) < PreludeSize {
		return false
	}
	total := int(binary.BigEndian.Uint32(d.buf[0:4]))
	if total >= MinMessageSize && total <= len(d.buf) {
		d.consume(total)
		d.bytesSkipped += total
		return true
	}
	return false
}

// skipByte 丢弃一个字节,尝试对齐到下一个可能的帧边界。
func (d *Decoder) skipByte() {
	d.consume(1)
	d.bytesSkipped++
}
