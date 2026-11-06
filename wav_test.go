package mp3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func wav(rate, channels int, pcm []int16) []byte {
	var b bytes.Buffer
	data := 2 * len(pcm)
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, uint32(36+8+4+data))
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, struct {
		Size                  uint32
		Format, Channels      uint16
		Rate, ByteRate        uint32
		BlockAlign, BitsDepth uint16
	}{16, 1, uint16(channels), uint32(rate), uint32(rate * channels * 2), uint16(channels * 2), 16})
	b.WriteString("LIST")
	binary.Write(&b, binary.LittleEndian, uint32(4))
	b.WriteString("INFO")
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, uint32(data))
	binary.Write(&b, binary.LittleEndian, pcm)
	return b.Bytes()
}

func TestEncodeWAV(t *testing.T) {
	pcm := tone(22050, 1, 0.5, 1000, 1500)
	var out bytes.Buffer
	if err := EncodeWAV(&out, bytes.NewReader(wav(22050, 2, pcm)), 96); err != nil {
		t.Fatal(err)
	}
	dec, rate := decode(t, out.Bytes())
	if rate != 22050 {
		t.Fatalf("sample rate = %d", rate)
	}
	mid := dec[1][len(dec[1])/4 : len(dec[1])*3/4]
	if f := float64(crossings(mid)) * 22050 / float64(len(mid)); f < 1490 || f > 1510 {
		t.Errorf("right channel = %.0f Hz, want 1500", f)
	}
}

func TestEncodeWAVErrors(t *testing.T) {
	floatWAV := wav(44100, 1, make([]int16, 100))
	floatWAV[20] = 3
	for name, in := range map[string][]byte{
		"empty":     nil,
		"not riff":  []byte(strings.Repeat("x", 64)),
		"float":     floatWAV,
		"truncated": wav(44100, 1, nil)[:30],
	} {
		if err := EncodeWAV(io.Discard, bytes.NewReader(in), 0); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if err := EncodeWAV(io.Discard, bytes.NewReader(wav(11025, 1, nil)), 0); err == nil {
		t.Error("11025 Hz accepted")
	}
}
