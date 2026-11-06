// Package mp3 implements a pure Go MPEG-1 and MPEG-2 Layer III encoder.
//
// The encoder produces constant bitrate streams using long blocks only, a
// bit reservoir and mid/side stereo. When the destination is an
// io.WriteSeeker an Info header is written so players know the duration.
package mp3

import (
	"errors"
	"fmt"
	"io"
	"math"
)

// ErrClosed is returned when writing to a closed Encoder.
var ErrClosed = errors.New("mp3: encoder closed")

// Options configures an Encoder.
type Options struct {
	// SampleRate is 32000, 44100 or 48000 for MPEG-1, or 16000, 22050 or
	// 24000 for MPEG-2.
	SampleRate int
	// Channels is 1 or 2.
	Channels int
	// Bitrate in kbit/s. Zero selects 128 for MPEG-1 and 64 for MPEG-2.
	Bitrate int
}

// Encoder encodes interleaved 16-bit PCM to MP3. It is not safe for
// concurrent use.
type Encoder struct {
	w        io.Writer
	seeker   io.WriteSeeker
	infoPos  int64
	lsf      bool
	channels int
	granules int
	rateIdx  int
	rateBits int
	bands    *[23]int
	sideLen  int
	maxBegin int
	cutoff   int

	frameBase int
	padStep   int
	padAcc    int
	rate      int

	pcm     [2][1152]float64
	n       int
	samples int64
	bank    [2]analyzer
	q       *quantizer
	xr      [2][2][576]float64
	gr      [2][2]granule

	main    bitWriter
	areaEnd int
	pending []pendingFrame
	frames  int
	size    int64
	started bool
	closed  bool
	err     error
}

type pendingFrame struct {
	head  []byte
	start int
	size  int
}

// NewEncoder returns an Encoder that writes MP3 frames to w.
func NewEncoder(w io.Writer, opts Options) (*Encoder, error) {
	e := &Encoder{w: w, rate: opts.SampleRate, channels: opts.Channels}

	version := -1
	for v, rates := range sampleRates {
		for i, r := range rates {
			if r == opts.SampleRate {
				version, e.rateIdx = v, i
			}
		}
	}
	if version < 0 {
		return nil, fmt.Errorf("mp3: unsupported sample rate %d", opts.SampleRate)
	}
	if opts.Channels != 1 && opts.Channels != 2 {
		return nil, fmt.Errorf("mp3: unsupported channel count %d", opts.Channels)
	}
	e.lsf = version == 1

	kbps := opts.Bitrate
	if kbps == 0 {
		kbps = 128
		if e.lsf {
			kbps = 64
		}
	}
	for i, b := range bitrates[version] {
		if b == kbps && i > 0 {
			e.rateBits = i
		}
	}
	if e.rateBits == 0 {
		return nil, fmt.Errorf("mp3: unsupported bitrate %d kbit/s at %d Hz", kbps, opts.SampleRate)
	}

	slots := 144
	e.granules, e.maxBegin = 2, 511
	if e.lsf {
		e.granules, e.maxBegin, slots = 1, 255, 72
	}
	e.sideLen = sideInfoLen(e.lsf, e.channels)
	e.frameBase = slots * kbps * 1000 / e.rate
	e.padStep = slots * kbps * 1000 % e.rate
	e.bands = &sfBands[version][e.rateIdx]
	e.q = newQuantizer(e.bands, e.rate, e.lsf)

	// Spend low bitrates on the audible range rather than the top octave.
	cutoff := 5500 + 180*float64(kbps)/float64(e.channels)
	e.cutoff = min(576, int(math.Ceil(cutoff/float64(e.rate)*1152)))

	if s, ok := w.(io.WriteSeeker); ok {
		if pos, err := s.Seek(0, io.SeekCurrent); err == nil {
			e.seeker, e.infoPos = s, pos
		}
	}
	return e, nil
}

// Write encodes interleaved samples. len(pcm) must be a multiple of the
// channel count.
func (e *Encoder) Write(pcm []int16) error {
	if e.closed {
		return ErrClosed
	}
	if e.err != nil {
		return e.err
	}
	if len(pcm)%e.channels != 0 {
		return fmt.Errorf("mp3: %d samples is not a multiple of %d channels", len(pcm), e.channels)
	}
	e.samples += int64(len(pcm) / e.channels)
	frameLen := 576 * e.granules
	for i := 0; i < len(pcm); i += e.channels {
		for ch := 0; ch < e.channels; ch++ {
			e.pcm[ch][e.n] = float64(pcm[i+ch]) / 32768
		}
		e.n++
		if e.n == frameLen {
			e.n = 0
			if err := e.encodeFrame(); err != nil {
				return err
			}
		}
	}
	return nil
}

// Close flushes buffered audio and, for seekable writers, fills in the Info
// header. It does not close the underlying writer.
func (e *Encoder) Close() error {
	if e.closed {
		return e.err
	}
	e.closed = true
	if e.err != nil || e.samples == 0 {
		return e.err
	}

	// Push the last samples through the filterbank and MDCT delay.
	frameLen := 576 * e.granules
	for pad := 0; e.n > 0 || pad < 1152; {
		pad += frameLen - e.n
		clear(e.pcm[0][e.n:frameLen])
		clear(e.pcm[1][e.n:frameLen])
		e.n = 0
		if err := e.encodeFrame(); err != nil {
			return err
		}
	}
	for len(e.main.buf) < e.areaEnd {
		e.main.buf = append(e.main.buf, 0)
	}
	if err := e.flush(); err != nil {
		return err
	}
	if e.seeker == nil {
		return nil
	}
	return e.writeInfo()
}

func (e *Encoder) encodeFrame() error {
	for gr := 0; gr < e.granules; gr++ {
		for ch := 0; ch < e.channels; ch++ {
			e.bank[ch].granule(e.pcm[ch][gr*576:], &e.xr[gr][ch])
		}
	}
	ms := e.channels == 2 && e.midSide()
	for gr := 0; gr < e.granules; gr++ {
		for ch := 0; ch < e.channels; ch++ {
			clear(e.xr[gr][ch][e.cutoff:])
		}
	}

	pad := 0
	if e.padAcc += e.padStep; e.padAcc >= e.rate {
		e.padAcc -= e.rate
		pad = 1
	}
	area := e.frameBase + pad - 4 - e.sideLen
	begin := e.areaEnd - len(e.main.buf)
	start := e.areaEnd
	e.areaEnd += area

	mean := area * 8 / (e.granules * e.channels)
	pool := begin * 8
	maxBits := min(4095, 7680/e.channels)
	for gr := 0; gr < e.granules; gr++ {
		for ch := 0; ch < e.channels; ch++ {
			g, xr := &e.gr[gr][ch], &e.xr[gr][ch]
			e.q.quantize(g, xr, min(mean+pool/2, maxBits))
			e.q.writeScalefactors(&e.main, g)
			writeHuffman(&e.main, g, xr[:], e.bands)
			pool += mean - g.part23
		}
	}
	e.main.align()
	for e.areaEnd-len(e.main.buf) > e.maxBegin {
		e.main.buf = append(e.main.buf, 0)
	}

	modeExt := 0
	if ms {
		modeExt = 2
	}
	var w bitWriter
	w.write(e.header(e.rateBits, pad, modeExt), 32)
	e.writeSideInfo(&w, begin)
	e.pending = append(e.pending, pendingFrame{head: w.buf, start: start, size: area})
	return e.flush()
}

// midSide converts the frame to mid/side when the channels are similar
// enough for it to save bits.
func (e *Encoder) midSide() bool {
	var m, s float64
	for gr := 0; gr < e.granules; gr++ {
		for i, l := range e.xr[gr][0] {
			r := e.xr[gr][1][i]
			m += (l + r) * (l + r)
			s += (l - r) * (l - r)
		}
	}
	if s >= 0.35*m {
		return false
	}
	for gr := 0; gr < e.granules; gr++ {
		for i, l := range e.xr[gr][0] {
			r := e.xr[gr][1][i]
			e.xr[gr][0][i] = (l + r) * math.Sqrt2 / 2
			e.xr[gr][1][i] = (l - r) * math.Sqrt2 / 2
		}
	}
	return true
}

func sideInfoLen(lsf bool, channels int) int {
	switch {
	case lsf && channels == 1:
		return 9
	case lsf, channels == 1:
		return 17
	}
	return 32
}

func (e *Encoder) header(rateBits, pad, modeExt int) uint32 {
	version := uint32(3)
	if e.lsf {
		version = 2
	}
	mode := uint32(1)
	if e.channels == 1 {
		mode = 3
	}
	return 0xFFE00000 | version<<19 | 1<<17 | 1<<16 |
		uint32(rateBits)<<12 | uint32(e.rateIdx)<<10 | uint32(pad)<<9 |
		mode<<6 | uint32(modeExt)<<4
}

func (e *Encoder) writeSideInfo(w *bitWriter, begin int) {
	if e.lsf {
		w.write(uint32(begin), 8)
		w.write(0, e.channels)
	} else {
		w.write(uint32(begin), 9)
		if e.channels == 1 {
			w.write(0, 5)
		} else {
			w.write(0, 3)
		}
		w.write(0, 4*e.channels)
	}
	for gr := 0; gr < e.granules; gr++ {
		for ch := 0; ch < e.channels; ch++ {
			g := &e.gr[gr][ch]
			w.write(uint32(g.part23), 12)
			w.write(uint32(g.bigValues), 9)
			w.write(uint32(g.globalGain), 8)
			if e.lsf {
				w.write(uint32(g.sfCompress), 9)
			} else {
				w.write(uint32(g.sfCompress), 4)
			}
			w.write(0, 1)
			for _, t := range g.table {
				w.write(uint32(t), 5)
			}
			w.write(uint32(g.region0), 4)
			w.write(uint32(g.region1), 3)
			if !e.lsf {
				w.write(0, 1)
			}
			w.write(0, 1)
			w.write(uint32(g.count1Table), 1)
		}
	}
}

// flush writes every pending frame whose main data is complete.
func (e *Encoder) flush() error {
	done := 0
	for len(e.pending) > 0 {
		f := e.pending[0]
		end := f.start + f.size
		if end > len(e.main.buf) {
			break
		}
		if !e.started && e.seeker != nil {
			if err := e.emit(e.infoFrame()); err != nil {
				return err
			}
		}
		e.started = true
		frame := append(f.head, e.main.buf[f.start:end]...)
		if err := e.emit(frame); err != nil {
			return err
		}
		e.frames++
		e.pending = e.pending[1:]
		done = end
	}
	if done > 0 {
		e.main.buf = append(e.main.buf[:0], e.main.buf[done:]...)
		e.areaEnd -= done
		for i := range e.pending {
			e.pending[i].start -= done
		}
	}
	return nil
}

func (e *Encoder) emit(b []byte) error {
	n, err := e.w.Write(b)
	e.size += int64(n)
	if err != nil {
		e.err = err
	}
	return err
}

// infoFrame returns a silent frame carrying an Info tag with the frame and
// byte counts so far. It uses the smallest bitrate at or above the stream's
// that leaves room for the tag.
func (e *Encoder) infoFrame() []byte {
	version, slots := 0, 144
	if e.lsf {
		version, slots = 1, 72
	}
	rateBits := e.rateBits
	size := 0
	for ; rateBits < 15; rateBits++ {
		size = slots * bitrates[version][rateBits] * 1000 / e.rate
		if size >= 4+e.sideLen+16 {
			break
		}
	}
	b := make([]byte, size)
	put32(b, e.header(rateBits, 0, 0))
	tag := b[4+e.sideLen:]
	copy(tag, "Info")
	put32(tag[4:], 3)
	put32(tag[8:], uint32(e.frames))
	put32(tag[12:], uint32(e.size))
	return b
}

func (e *Encoder) writeInfo() error {
	end, err := e.seeker.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = e.seeker.Seek(e.infoPos, io.SeekStart)
	}
	if err == nil {
		_, err = e.seeker.Write(e.infoFrame())
	}
	if err == nil {
		_, err = e.seeker.Seek(end, io.SeekStart)
	}
	if err != nil {
		e.err = err
	}
	return err
}

func put32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}
