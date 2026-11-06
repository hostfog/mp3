package mp3

import "math"

var (
	analysisWindow [512]float64
	analysisMatrix [32][64]float64
	mdctTable      [18][36]float64
	aliasCS        [8]float64
	aliasCA        [8]float64
)

func init() {
	for i, d := range enwindow {
		analysisWindow[i] = float64(d) / (65536 * 32)
	}
	for k := range analysisMatrix {
		for i := range analysisMatrix[k] {
			analysisMatrix[k][i] = math.Cos(float64((2*k+1)*(i-16)) * math.Pi / 64)
		}
	}
	for k := range mdctTable {
		for n := range mdctTable[k] {
			w := math.Sin(math.Pi / 36 * (float64(n) + 0.5))
			mdctTable[k][n] = w * math.Cos(math.Pi/72*float64((2*n+19)*(2*k+1))) / 9
		}
	}
	for i, c := range [8]float64{-0.6, -0.535, -0.33, -0.185, -0.095, -0.041, -0.0142, -0.0037} {
		s := math.Sqrt(1 + c*c)
		aliasCS[i] = 1 / s
		aliasCA[i] = c / s
	}
}

// analyzer holds the per-channel state of the polyphase filterbank and MDCT.
type analyzer struct {
	x    [512]float64
	prev [32][18]float64
}

// granule turns 576 samples into 576 MDCT coefficients.
func (a *analyzer) granule(pcm []float64, xr *[576]float64) {
	var cur [32][18]float64
	var y [64]float64
	for t := 0; t < 18; t++ {
		copy(a.x[32:], a.x[:480])
		for i := 0; i < 32; i++ {
			a.x[31-i] = pcm[t*32+i]
		}
		for i := range y {
			var s float64
			for j := i; j < 512; j += 64 {
				s += analysisWindow[j] * a.x[j]
			}
			y[i] = s
		}
		for k := 0; k < 32; k++ {
			var s float64
			m := &analysisMatrix[k]
			for i, v := range y {
				s += m[i] * v
			}
			if k&1 == 1 && t&1 == 1 {
				s = -s
			}
			cur[k][t] = s
		}
	}

	var in [36]float64
	for sb := 0; sb < 32; sb++ {
		copy(in[:18], a.prev[sb][:])
		copy(in[18:], cur[sb][:])
		out := xr[sb*18 : sb*18+18]
		for k := range out {
			var s float64
			for n, v := range in {
				s += mdctTable[k][n] * v
			}
			out[k] = s
		}
	}
	a.prev = cur

	for sb := 1; sb < 32; sb++ {
		for i := 0; i < 8; i++ {
			lo, hi := sb*18-1-i, sb*18+i
			p, q := xr[lo], xr[hi]
			xr[lo] = p*aliasCS[i] + q*aliasCA[i]
			xr[hi] = q*aliasCS[i] - p*aliasCA[i]
		}
	}
}
