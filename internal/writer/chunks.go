package writer

// chunkSize is the size of the blocks a Chunks fills. Large enough that
// a stream of hundreds of megabytes is a few hundred blocks; small enough
// that the unused tail of the last one does not matter.
const chunkSize = 1 << 20

// Chunks is an io.Writer that keeps what is written in fixed-size blocks.
// Unlike a bytes.Buffer it never copies what it holds to grow, and never
// needs twice its size while growing: in a WebAssembly heap, which cannot
// shrink, that peak would stay allocated for the life of the page.
//
// Its blocks can be passed to PutStream as they are.
type Chunks struct {
	blocks [][]byte
}

// Write appends p. It never fails.
func (c *Chunks) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		if len(c.blocks) == 0 || len(c.blocks[len(c.blocks)-1]) == chunkSize {
			c.blocks = append(c.blocks, make([]byte, 0, min(chunkSize, max(len(p), 4096))))
		}
		last := &c.blocks[len(c.blocks)-1]
		if cap(*last)-len(*last) < min(len(p), chunkSize-len(*last)) {
			// The first block starts small for small streams; grow it
			// to full size once rather than in steps.
			grown := make([]byte, len(*last), chunkSize)
			copy(grown, *last)
			*last = grown
		}
		k := min(len(p), chunkSize-len(*last))
		*last = append(*last, p[:k]...)
		p = p[k:]
	}
	return n, nil
}

// Blocks returns what was written, in order.
func (c *Chunks) Blocks() [][]byte { return c.blocks }

// Len returns the number of bytes written.
func (c *Chunks) Len() int {
	n := 0
	for _, b := range c.blocks {
		n += len(b)
	}
	return n
}
