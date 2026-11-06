package mp3

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ErrFormat is returned by EncodeWAV for input that is not 16-bit PCM WAV.
var ErrFormat = errors.New("mp3: not a 16-bit PCM WAV stream")

// EncodeWAV reads a 16-bit PCM WAV stream from r and writes it to w as MP3
// at the given bitrate in kbit/s, or the default when bitrate is zero. The
// sample rate and channel count are taken from the WAV header.
func EncodeWAV(w io.Writer, r io.Reader, bitrate int) error {
	br := bufio.NewReader(r)
	var riff [12]byte
	if _, err := io.ReadFull(br, riff[:]); err != nil || string(riff[:4]) != "RIFF" || string(riff[8:]) != "WAVE" {
		return ErrFormat
	}

	var opts Options
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return ErrFormat
		}
		size := int64(binary.LittleEndian.Uint32(hdr[4:]))
		switch string(hdr[:4]) {
		case "fmt ":
			if size < 16 {
				return ErrFormat
			}
			fmtChunk := make([]byte, size+size&1)
			if _, err := io.ReadFull(br, fmtChunk); err != nil {
				return ErrFormat
			}
			tag := binary.LittleEndian.Uint16(fmtChunk)
			if tag == 0xFFFE && size >= 26 {
				tag = binary.LittleEndian.Uint16(fmtChunk[24:])
			}
			if tag != 1 || binary.LittleEndian.Uint16(fmtChunk[14:]) != 16 {
				return ErrFormat
			}
			opts.Channels = int(binary.LittleEndian.Uint16(fmtChunk[2:]))
			opts.SampleRate = int(binary.LittleEndian.Uint32(fmtChunk[4:]))
		case "data":
			if opts.Channels == 0 {
				return ErrFormat
			}
			opts.Bitrate = bitrate
			var data io.Reader = br
			if size != 0 && size != 0xFFFFFFFF {
				data = io.LimitReader(br, size)
			}
			return encodePCM(w, data, opts)
		default:
			if _, err := io.CopyN(io.Discard, br, size+size&1); err != nil {
				return ErrFormat
			}
		}
	}
}

func encodePCM(w io.Writer, r io.Reader, opts Options) error {
	enc, err := NewEncoder(w, opts)
	if err != nil {
		return err
	}
	buf := make([]byte, 4096*opts.Channels)
	pcm := make([]int16, len(buf)/2)
	for {
		n, err := io.ReadFull(r, buf)
		n -= n % (2 * opts.Channels)
		for i := 0; i < n/2; i++ {
			pcm[i] = int16(binary.LittleEndian.Uint16(buf[2*i:]))
		}
		if werr := enc.Write(pcm[:n/2]); werr != nil {
			return werr
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return enc.Close()
		}
		if err != nil {
			return fmt.Errorf("mp3: reading WAV data: %w", err)
		}
	}
}
