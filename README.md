# mp3

Pure Go MP3 encoder. No cgo, no dependencies outside the standard library.

```sh
go get github.com/hostfog/mp3
```

## Usage

```go
enc, err := mp3.NewEncoder(w, mp3.Options{SampleRate: 44100, Channels: 2, Bitrate: 128})
if err != nil {
	return err
}
if err := enc.Write(pcm); err != nil { // interleaved int16
	return err
}
return enc.Close()
```

From a WAV file:

```go
err := mp3.EncodeWAV(dst, src, 192)
```

Or on the command line:

```sh
go install github.com/hostfog/mp3/cmd/wav2mp3@latest
wav2mp3 -b 192 in.wav out.mp3
```

## Formats

| Version | Sample rates (Hz)     | Bitrates (kbit/s) |
|---------|-----------------------|-------------------|
| MPEG-1  | 32000, 44100, 48000   | 32 – 320          |
| MPEG-2  | 16000, 22050, 24000   | 8 – 160           |

Mono or stereo, constant bitrate. Stereo frames switch to mid/side when the channels are similar.

## Design

- **Layer III, long blocks only.** Polyphase filterbank, 18-point MDCT with alias reduction, Huffman coding with per-region table selection and a bit reservoir.
- **Simple psychoacoustics.** Allowed noise per scalefactor band is the larger of the threshold of hearing and a fixed fraction of the band's energy. The outer loop raises scalefactors until the noise fits or the bits run out. There is no transient detection, so sharp attacks can pre-echo at low bitrates.
- **Info header.** When the writer is an `io.WriteSeeker` (such as `*os.File`) the first frame carries frame and byte counts so players show the right duration.
- **Quality.** A 1 kHz sine round-trips at about 80 dB SNR at 128 kbit/s. Encoding runs at roughly 40× real time on one core.

## License

MIT. The Huffman tables and filterbank window are ISO 11172-3 data taken from [go-mp3](https://github.com/hajimehoshi/go-mp3) (Apache-2.0); see [NOTICE](NOTICE).
