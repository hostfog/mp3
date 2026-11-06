package mp3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	gomp3 "github.com/hajimehoshi/go-mp3"
)

// tone returns interleaved sine waves, one frequency per channel.
func tone(rate int, seconds, amp float64, freqs ...float64) []int16 {
	n := int(seconds * float64(rate))
	pcm := make([]int16, n*len(freqs))
	for i := 0; i < n; i++ {
		for ch, f := range freqs {
			pcm[i*len(freqs)+ch] = int16(amp * 32767 * math.Sin(2*math.Pi*f*float64(i)/float64(rate)))
		}
	}
	return pcm
}

func encode(t testing.TB, w io.Writer, opts Options, pcm []int16) {
	t.Helper()
	enc, err := NewEncoder(w, opts)
	if err != nil {
		t.Fatal(err)
	}
	for len(pcm) > 0 {
		n := min(len(pcm), 4410*opts.Channels)
		if err := enc.Write(pcm[:n]); err != nil {
			t.Fatal(err)
		}
		pcm = pcm[n:]
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
}

// decode returns both channels of an MP3 stream decoded by go-mp3.
func decode(t *testing.T, data []byte) ([2][]float64, int) {
	t.Helper()
	d, err := gomp3.NewDecoder(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(d)
	if err != nil {
		t.Fatal(err)
	}
	var out [2][]float64
	for i := 0; i+4 <= len(raw); i += 4 {
		for ch := range out {
			v := int16(binary.LittleEndian.Uint16(raw[i+2*ch:]))
			out[ch] = append(out[ch], float64(v)/32768)
		}
	}
	return out, d.SampleRate()
}

// codecDelay is the delay in samples of the analysis and synthesis
// filterbanks plus one granule of MDCT overlap.
const codecDelay = 1057

// snr returns the signal to noise ratio of dec against src in dB over the
// steady middle of the signal, allowing a little slack around codecDelay.
func snr(src, dec []float64) float64 {
	lo, hi := len(src)/4, len(src)*3/4
	best := math.Inf(-1)
	for lag := codecDelay - 8; lag <= codecDelay+8 && hi+lag <= len(dec); lag++ {
		var s, n float64
		for i := lo; i < hi; i++ {
			d := dec[i+lag] - src[i]
			s += src[i] * src[i]
			n += d * d
		}
		best = math.Max(best, 10*math.Log10(s/n))
	}
	return best
}

func crossings(x []float64) int {
	n := 0
	for i := 1; i < len(x); i++ {
		if x[i-1] < 0 && x[i] >= 0 {
			n++
		}
	}
	return n
}

type frameInfo struct {
	count int
	info  bool
}

// frames walks the stream header by header, checking sync and sizes.
func frames(data []byte) (frameInfo, error) {
	var fi frameInfo
	for pos := 0; pos < len(data); {
		if len(data)-pos < 4 {
			return fi, fmt.Errorf("trailing %d bytes", len(data)-pos)
		}
		h := uint32(data[pos])<<24 | uint32(data[pos+1])<<16 | uint32(data[pos+2])<<8 | uint32(data[pos+3])
		if h&0xFFE00000 != 0xFFE00000 || h>>17&3 != 1 {
			return fi, fmt.Errorf("lost sync at %d", pos)
		}
		version := 0
		slots := 144
		if h>>19&3 == 2 {
			version, slots = 1, 72
		}
		rate := sampleRates[version][h>>10&3]
		size := slots*bitrates[version][h>>12&15]*1000/rate + int(h>>9&1)
		channels := 2
		if h>>6&3 == 3 {
			channels = 1
		}
		side := sideInfoLen(version == 1, channels)
		if pos == 0 && bytes.HasPrefix(data[4+side:], []byte("Info")) {
			fi.info = true
		} else {
			fi.count++
		}
		pos += size
	}
	return fi, nil
}

func TestRoundTrip(t *testing.T) {
	layouts := map[string][]float64{
		"mono":   {1000},
		"stereo": {1000, 1500},
		"joint":  {1000, 1000},
	}
	for _, rate := range []int{32000, 44100, 48000, 16000, 22050, 24000} {
		for name, freqs := range layouts {
			t.Run(fmt.Sprintf("%d/%s", rate, name), func(t *testing.T) {
				channels := len(freqs)
				pcm := tone(rate, 1, 0.5, freqs...)
				var buf bytes.Buffer
				encode(t, &buf, Options{SampleRate: rate, Channels: channels}, pcm)

				fi, err := frames(buf.Bytes())
				if err != nil {
					t.Fatal(err)
				}
				perFrame := 1152
				if rate < 32000 {
					perFrame = 576
				}
				samples := len(pcm) / channels
				if want := (samples + 1152 + perFrame - 1) / perFrame; fi.count != want {
					t.Errorf("frames = %d, want %d", fi.count, want)
				}

				dec, decRate := decode(t, buf.Bytes())
				if decRate != rate {
					t.Fatalf("sample rate = %d", decRate)
				}
				for ch, f := range freqs {
					src := make([]float64, samples)
					for i := range src {
						src[i] = float64(pcm[i*channels+ch]) / 32768
					}
					q := snr(src, dec[ch])
					mid := dec[ch][len(dec[ch])/4 : len(dec[ch])*3/4]
					got := float64(crossings(mid)) * float64(rate) / float64(len(mid))
					t.Logf("channel %d: snr %.1f dB, %.0f Hz", ch, q, got)
					if math.Abs(got-f) > f/200 {
						t.Errorf("channel %d: frequency = %.1f Hz, want %.0f", ch, got, f)
					}
					if q < 40 {
						t.Errorf("channel %d: snr = %.1f dB", ch, q)
					}
				}
			})
		}
	}
}

func TestInfoHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.mp3")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encode(t, f, Options{SampleRate: 44100, Channels: 2, Bitrate: 192}, tone(44100, 2, 0.3, 440, 660))
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := frames(data)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.info {
		t.Fatal("no Info header")
	}
	tag := data[4+32:]
	count := int(tag[8])<<24 | int(tag[9])<<16 | int(tag[10])<<8 | int(tag[11])
	size := int(tag[12])<<24 | int(tag[13])<<16 | int(tag[14])<<8 | int(tag[15])
	if count != fi.count || size != len(data) {
		t.Errorf("Info says %d frames, %d bytes; stream has %d, %d", count, size, fi.count, len(data))
	}
}

func TestOptions(t *testing.T) {
	bad := []Options{
		{SampleRate: 11025, Channels: 1},
		{SampleRate: 44100, Channels: 3},
		{SampleRate: 44100, Channels: 2, Bitrate: 8},
		{SampleRate: 22050, Channels: 2, Bitrate: 320},
	}
	for _, o := range bad {
		if _, err := NewEncoder(io.Discard, o); err == nil {
			t.Errorf("NewEncoder(%+v) succeeded", o)
		}
	}
	for v, rates := range sampleRates {
		for _, rate := range rates {
			for _, kbps := range bitrates[v][1:] {
				if _, err := NewEncoder(io.Discard, Options{SampleRate: rate, Channels: 2, Bitrate: kbps}); err != nil {
					t.Error(err)
				}
			}
		}
	}
}

// TestBitrates checks every bitrate against ffmpeg when it is installed.
// go-mp3 is not used here because it miscomputes the size of padded MPEG-2
// frames.
func TestBitrates(t *testing.T) {
	ffmpeg, _ := exec.LookPath("ffmpeg")
	for v, rates := range sampleRates {
		for _, kbps := range bitrates[v][1:] {
			for _, freqs := range [][]float64{{1000}, {1000, 1500}} {
				rate := rates[0]
				opts := Options{SampleRate: rate, Channels: len(freqs), Bitrate: kbps}
				pcm := tone(rate, 1, 0.5, freqs...)
				var buf bytes.Buffer
				encode(t, &buf, opts, pcm)
				if _, err := frames(buf.Bytes()); err != nil {
					t.Fatalf("%+v: %v", opts, err)
				}
				if ffmpeg == "" {
					continue
				}

				cmd := exec.Command(ffmpeg, "-v", "error", "-i", "pipe:0", "-f", "s16le", "pipe:1")
				var stderr bytes.Buffer
				cmd.Stdin, cmd.Stderr = &buf, &stderr
				raw, err := cmd.Output()
				if err != nil || stderr.Len() > 0 {
					t.Fatalf("%+v: ffmpeg: %v %s", opts, err, stderr.Bytes())
				}
				for ch := range freqs {
					var src, dec []float64
					for i := ch; i < len(pcm); i += len(freqs) {
						src = append(src, float64(pcm[i])/32768)
						dec = append(dec, float64(int16(binary.LittleEndian.Uint16(raw[2*i:])))/32768)
					}
					q := snr(src, dec)
					t.Logf("%d Hz %d kbit/s %d channels: snr %.1f dB", rate, kbps, len(freqs), q)
					if kbps/len(freqs) >= 24 && q < 30 {
						t.Errorf("%+v: channel %d snr = %.1f dB", opts, ch, q)
					}
				}
			}
		}
	}
}

func noise(rate, channels int, seconds float64) []int16 {
	pcm := make([]int16, int(seconds*float64(rate))*channels)
	x := uint32(1)
	for i := range pcm {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		pcm[i] = int16(x >> 17)
	}
	return pcm
}

func TestWriteErrors(t *testing.T) {
	enc, err := NewEncoder(io.Discard, Options{SampleRate: 44100, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := enc.Write([]int16{1, 2, 3}); err == nil {
		t.Error("odd sample count accepted")
	}
	if err := enc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := enc.Write([]int16{1, 2}); !errors.Is(err, ErrClosed) {
		t.Errorf("Write after Close = %v", err)
	}
}

func BenchmarkEncode(b *testing.B) {
	pcm := tone(44100, 10, 0.5, 1000, 1500)
	for i, v := range noise(44100, 2, 10) {
		pcm[i] += v / 8
	}
	b.SetBytes(int64(len(pcm) * 2))
	for i := 0; i < b.N; i++ {
		encode(b, io.Discard, Options{SampleRate: 44100, Channels: 2, Bitrate: 128}, pcm)
	}
}
