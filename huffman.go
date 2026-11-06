package mp3

type huffTable struct {
	xlen    int
	linbits int
	codes   []uint32
	lens    []uint8
}

var huffTables [32]huffTable

func init() {
	small := []struct {
		n     int
		xlen  int
		codes []uint32
		lens  []uint8
	}{
		{1, 2, huffCodes1[:], huffLens1[:]},
		{2, 3, huffCodes2[:], huffLens2[:]},
		{3, 3, huffCodes3[:], huffLens3[:]},
		{5, 4, huffCodes5[:], huffLens5[:]},
		{6, 4, huffCodes6[:], huffLens6[:]},
		{7, 6, huffCodes7[:], huffLens7[:]},
		{8, 6, huffCodes8[:], huffLens8[:]},
		{9, 6, huffCodes9[:], huffLens9[:]},
		{10, 8, huffCodes10[:], huffLens10[:]},
		{11, 8, huffCodes11[:], huffLens11[:]},
		{12, 8, huffCodes12[:], huffLens12[:]},
		{13, 16, huffCodes13[:], huffLens13[:]},
		{15, 16, huffCodes15[:], huffLens15[:]},
	}
	for _, t := range small {
		huffTables[t.n] = huffTable{xlen: t.xlen, codes: t.codes, lens: t.lens}
	}
	for i, lb := range [8]int{1, 2, 3, 4, 6, 8, 10, 13} {
		huffTables[16+i] = huffTable{16, lb, huffCodes16[:], huffLens16[:]}
	}
	for i, lb := range [8]int{4, 5, 6, 7, 8, 9, 11, 13} {
		huffTables[24+i] = huffTable{16, lb, huffCodes24[:], huffLens24[:]}
	}
}

// Candidate tables for a region whose largest value fits in xlen-1.
var tableGroups = []struct {
	xlen   int
	tables []int
}{
	{2, []int{1}},
	{3, []int{2, 3}},
	{4, []int{5, 6}},
	{6, []int{7, 8, 9}},
	{8, []int{10, 11, 12}},
	{16, []int{13, 15}},
}

func pairBits(ix []int32, t int) int {
	h := &huffTables[t]
	bits := 0
	for i := 0; i < len(ix); i += 2 {
		x, y := int(ix[i]), int(ix[i+1])
		if x >= 15 && h.linbits > 0 {
			bits += h.linbits
			x = 15
		}
		if y >= 15 && h.linbits > 0 {
			bits += h.linbits
			y = 15
		}
		bits += int(h.lens[x*h.xlen+y])
		if x != 0 {
			bits++
		}
		if y != 0 {
			bits++
		}
	}
	return bits
}

func chooseTable(ix []int32) (table, bits int) {
	var max int32
	for _, v := range ix {
		if v > max {
			max = v
		}
	}
	if max == 0 {
		return 0, 0
	}
	if max < 16 {
		for _, g := range tableGroups {
			if int(max) < g.xlen {
				table, bits = -1, 0
				for _, t := range g.tables {
					if b := pairBits(ix, t); table < 0 || b < bits {
						table, bits = t, b
					}
				}
				return table, bits
			}
		}
	}
	table = -1
	for _, first := range [2]int{16, 24} {
		for t := first; t < first+8; t++ {
			if int(max)-15 < 1<<huffTables[t].linbits {
				if b := pairBits(ix, t); table < 0 || b < bits {
					table, bits = t, b
				}
				break
			}
		}
	}
	return table, bits
}

// countBits sets the Huffman side info of g from g.ix and returns the number
// of main data bits it needs, excluding scalefactors.
func countBits(g *granule, bands *[23]int) int {
	end := 576
	for end > 1 && g.ix[end-1] == 0 && g.ix[end-2] == 0 {
		end -= 2
	}
	g.count1End = end
	bitsA, bitsB := 0, 0
	for end > 3 {
		v, w, x, y := g.ix[end-4], g.ix[end-3], g.ix[end-2], g.ix[end-1]
		if v > 1 || w > 1 || x > 1 || y > 1 {
			break
		}
		signs := int(v + w + x + y)
		bitsA += int(huffLens32[v<<3|w<<2|x<<1|y]) + signs
		bitsB += 4 + signs
		end -= 4
	}
	g.bigValues = end / 2

	bits := bitsA
	g.count1Table = 0
	if bitsB < bitsA {
		bits = bitsB
		g.count1Table = 1
	}

	n := 0
	for bands[n] < end {
		n++
	}
	g.region0 = regionSplit[n][0]
	g.region1 = regionSplit[n][1]
	a1 := min(bands[g.region0+1], end)
	a2 := min(bands[g.region0+g.region1+2], end)

	for i, r := range [3][2]int{{0, a1}, {a1, a2}, {a2, end}} {
		t, b := chooseTable(g.ix[r[0]:r[1]])
		g.table[i] = t
		bits += b
	}
	return bits
}

func writePairs(w *bitWriter, ix []int32, xr []float64, t int) {
	if t == 0 {
		return
	}
	h := &huffTables[t]
	for i := 0; i < len(ix); i += 2 {
		x, y := int(ix[i]), int(ix[i+1])
		cx, cy := x, y
		if h.linbits > 0 {
			cx, cy = min(x, 15), min(y, 15)
		}
		k := cx*h.xlen + cy
		w.write(h.codes[k], int(h.lens[k]))
		if cx == 15 && h.linbits > 0 {
			w.write(uint32(x-15), h.linbits)
		}
		if x != 0 {
			w.write(sign(xr[i]), 1)
		}
		if cy == 15 && h.linbits > 0 {
			w.write(uint32(y-15), h.linbits)
		}
		if y != 0 {
			w.write(sign(xr[i+1]), 1)
		}
	}
}

func writeHuffman(w *bitWriter, g *granule, xr []float64, bands *[23]int) {
	end := g.bigValues * 2
	a1 := min(bands[g.region0+1], end)
	a2 := min(bands[g.region0+g.region1+2], end)
	writePairs(w, g.ix[:a1], xr[:a1], g.table[0])
	writePairs(w, g.ix[a1:a2], xr[a1:a2], g.table[1])
	writePairs(w, g.ix[a2:end], xr[a2:end], g.table[2])

	for i := end; i < g.count1End; i += 4 {
		q := g.ix[i : i+4]
		k := q[0]<<3 | q[1]<<2 | q[2]<<1 | q[3]
		if g.count1Table == 0 {
			w.write(huffCodes32[k], int(huffLens32[k]))
		} else {
			w.write(uint32(15-k), 4)
		}
		for j, v := range q {
			if v != 0 {
				w.write(sign(xr[i+j]), 1)
			}
		}
	}
}

func sign(v float64) uint32 {
	if v < 0 {
		return 1
	}
	return 0
}
