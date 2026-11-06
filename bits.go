package mp3

type bitWriter struct {
	buf []byte
	acc uint64
	n   uint
}

func (w *bitWriter) write(v uint32, n int) {
	if n == 0 {
		return
	}
	w.acc = w.acc<<uint(n) | uint64(v)&(1<<uint(n)-1)
	w.n += uint(n)
	for w.n >= 8 {
		w.n -= 8
		w.buf = append(w.buf, byte(w.acc>>w.n))
	}
}

func (w *bitWriter) align() {
	if w.n > 0 {
		w.write(0, int(8-w.n))
	}
}

func (w *bitWriter) len() int {
	return len(w.buf)*8 + int(w.n)
}
