package mp3

import "math"

// Scalefactor partitions for long blocks: MPEG-1 codes bands 0-10 and 11-20
// with two lengths, MPEG-2 uses four groups of 6, 5, 5 and 5 bands.
var (
	partitions    = []int{0, 11, 21}
	partitionsLSF = []int{0, 6, 11, 16, 21}
)

// maskRatio is the noise level, relative to the energy of a scalefactor band,
// that the distortion loop tries to stay under.
const maskRatio = 0.02

var (
	pow43       [8192]float64
	quantStep   [256]float64
	dequantStep [256]float64
	sfAmp       [16]float64
	sfAtt       [16]float64
)

func init() {
	for i := range pow43 {
		pow43[i] = math.Pow(float64(i), 4.0/3)
	}
	for i := range quantStep {
		quantStep[i] = math.Pow(2, -0.1875*float64(i-210))
		dequantStep[i] = math.Pow(2, 0.25*float64(i-210))
	}
	for i := range sfAmp {
		sfAmp[i] = math.Pow(2, 0.375*float64(i))
		sfAtt[i] = math.Pow(2, -0.5*float64(i))
	}
}

// granule is the quantised spectrum of one channel in one granule together
// with its side information.
type granule struct {
	ix          [576]int32
	sf          [21]int
	slen        [4]int
	part2       int
	part23      int
	bigValues   int
	count1End   int
	globalGain  int
	sfCompress  int
	table       [3]int
	region0     int
	region1     int
	count1Table int
}

type quantizer struct {
	bands   *[23]int
	lsf     bool
	ath     [21]float64
	xr34    [576]float64
	allowed [21]float64
	noise   [21]float64
	best    granule
}

func newQuantizer(bands *[23]int, rate int, lsf bool) *quantizer {
	q := &quantizer{bands: bands, lsf: lsf}
	for sfb := range q.ath {
		lo := math.Inf(1)
		for i := bands[sfb]; i < bands[sfb+1]; i++ {
			lo = math.Min(lo, ath((float64(i)+0.5)*float64(rate)/1152))
		}
		q.ath[sfb] = lo * float64(bands[sfb+1]-bands[sfb])
	}
	return q
}

// ath returns the absolute threshold of hearing at f Hz as MDCT line energy,
// placing the most sensitive point near the 16-bit noise floor.
func ath(f float64) float64 {
	k := math.Max(f, 20) / 1000
	db := 3.64*math.Pow(k, -0.8) - 6.5*math.Exp(-0.6*(k-3.3)*(k-3.3)) + 1e-3*k*k*k*k
	return math.Pow(10, (db-96)/10)
}

// quantize fills g from xr using at most budget bits. The inner loop finds the
// smallest global gain that fits; the outer loop raises scalefactors in bands
// whose noise exceeds the allowed level and keeps the best result.
func (q *quantizer) quantize(g *granule, xr *[576]float64, budget int) {
	*g = granule{}
	var peak float64
	for i, v := range xr {
		a := math.Abs(v)
		q.xr34[i] = math.Sqrt(a * math.Sqrt(a))
		peak = math.Max(peak, q.xr34[i])
	}
	if peak == 0 {
		return
	}
	for sfb := range q.allowed {
		var e float64
		for _, v := range xr[q.bands[sfb]:q.bands[sfb+1]] {
			e += v * v
		}
		q.allowed[sfb] = math.Max(q.ath[sfb], e*maskRatio)
	}

	gain := max(0, int(math.Ceil(210-math.Log2(8191/peak)/0.1875)))
	found := false
	var bestOver int
	var bestNoise float64
	for {
		q.setPart2(g)
		if g.part2 > budget {
			break
		}
		bits, ok := q.search(g, budget-g.part2, gain)
		if !ok {
			break
		}
		gain = g.globalGain
		g.part23 = g.part2 + bits

		over, noise := q.distortion(g, xr)
		if !found || over < bestOver || over == bestOver && noise < bestNoise {
			q.best = *g
			found, bestOver, bestNoise = true, over, noise
		}
		if over == 0 || !q.amplify(g) {
			break
		}
	}
	if found {
		*g = q.best
	} else {
		*g = granule{}
	}
}

func (q *quantizer) quantizeAt(g *granule) bool {
	step := quantStep[g.globalGain]
	for sfb := 0; sfb < 22; sfb++ {
		s := step
		if sfb < 21 {
			s *= sfAmp[g.sf[sfb]]
		}
		for i := q.bands[sfb]; i < q.bands[sfb+1]; i++ {
			v := q.xr34[i] * s
			if v > 8191.5 {
				return false
			}
			g.ix[i] = int32(v + 0.4054)
		}
	}
	return true
}

// search returns the Huffman bits for the smallest global gain of at least
// lo whose spectrum fits in budget.
func (q *quantizer) search(g *granule, budget, lo int) (int, bool) {
	try := func(gain int) (int, bool) {
		g.globalGain = gain
		if !q.quantizeAt(g) {
			return 0, false
		}
		bits := countBits(g, q.bands)
		return bits, bits <= budget
	}
	if bits, ok := try(lo); ok {
		return bits, true
	}
	bad, good := lo, lo
	for step := 1; ; step *= 2 {
		good = min(bad+step, 255)
		if _, ok := try(good); ok {
			break
		}
		if good == 255 {
			return 0, false
		}
		bad = good
	}
	for good-bad > 1 {
		mid := (bad + good) / 2
		if _, ok := try(mid); ok {
			good = mid
		} else {
			bad = mid
		}
	}
	return try(good)
}

func (q *quantizer) distortion(g *granule, xr *[576]float64) (over int, total float64) {
	step := dequantStep[g.globalGain]
	for sfb := range q.noise {
		s := step * sfAtt[g.sf[sfb]]
		var n float64
		for i := q.bands[sfb]; i < q.bands[sfb+1]; i++ {
			d := math.Abs(xr[i]) - pow43[g.ix[i]]*s
			n += d * d
		}
		q.noise[sfb] = n
		if n > q.allowed[sfb] {
			over++
			total += math.Log10(n / q.allowed[sfb])
		}
	}
	return over, total
}

// amplify raises the scalefactor of every band with too much noise. It
// reports false once a scalefactor runs out of range or all bands have been
// raised, since further steps would only shift the global gain.
func (q *quantizer) amplify(g *granule) bool {
	all := true
	for sfb := range g.sf {
		if q.noise[sfb] > q.allowed[sfb] {
			g.sf[sfb]++
			limit := 15
			if sfb >= 11 {
				limit = 7
			}
			if g.sf[sfb] > limit {
				return false
			}
		}
		if g.sf[sfb] == 0 {
			all = false
		}
	}
	return !all
}

func (q *quantizer) setPart2(g *granule) {
	parts := partitions
	if q.lsf {
		parts = partitionsLSF
	}
	g.slen = [4]int{}
	for p := 0; p+1 < len(parts); p++ {
		for _, v := range g.sf[parts[p]:parts[p+1]] {
			for v>>g.slen[p] != 0 {
				g.slen[p]++
			}
		}
	}

	if q.lsf {
		s := g.slen
		g.sfCompress = (s[0]*5+s[1])<<4 | s[2]<<2 | s[3]
		g.part2 = 6*s[0] + 5*(s[1]+s[2]+s[3])
		return
	}
	best := -1
	for c, s := range slen {
		if s[0] < g.slen[0] || s[1] < g.slen[1] {
			continue
		}
		if bits := 11*s[0] + 10*s[1]; best < 0 || bits < g.part2 {
			best, g.part2 = c, bits
		}
	}
	g.sfCompress = best
	g.slen[0], g.slen[1] = slen[best][0], slen[best][1]
}

func (q *quantizer) writeScalefactors(w *bitWriter, g *granule) {
	parts := partitions
	if q.lsf {
		parts = partitionsLSF
	}
	for p := 0; p+1 < len(parts); p++ {
		for _, v := range g.sf[parts[p]:parts[p+1]] {
			w.write(uint32(v), g.slen[p])
		}
	}
}
