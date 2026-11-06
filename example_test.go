package mp3_test

import (
	"bytes"
	"fmt"
	"log"
	"math"

	"github.com/hostfog/mp3"
)

func Example() {
	var out bytes.Buffer
	enc, err := mp3.NewEncoder(&out, mp3.Options{SampleRate: 44100, Channels: 2, Bitrate: 128})
	if err != nil {
		log.Fatal(err)
	}

	pcm := make([]int16, 2*44100)
	for i := 0; i < len(pcm); i += 2 {
		v := int16(8000 * math.Sin(2*math.Pi*440*float64(i/2)/44100))
		pcm[i], pcm[i+1] = v, v
	}
	if err := enc.Write(pcm); err != nil {
		log.Fatal(err)
	}
	if err := enc.Close(); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("%x\n", out.Bytes()[:2])
	// Output: fffb
}
