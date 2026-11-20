package pdfo

// Limits bounds what a broken or hostile file can make an operation spend.
// A zero field means its default; the defaults are far above real files.
type Limits struct {
	// MaxDepth bounds the nesting of arrays and dictionaries and the depth of
	// every tree: pages, bookmarks, form fields, name and number trees.
	// Default 100.
	MaxDepth int
	// MaxStreamSize bounds the decoded size of any one stream; it is what
	// stops a decompression bomb. Default 256 MiB.
	MaxStreamSize int64
	// MaxDecoded bounds the decoded bytes of all streams in one call, so many
	// small bombs cannot add up to a hang. Default 2 GiB.
	MaxDecoded int64
	// MaxImagePixels bounds the images that are decoded: the PNGs FromImages
	// embeds and, later, the images Compress resamples. JPEGs are embedded
	// without decoding, so it does not apply to them. Default 50 million,
	// which a 48-megapixel phone photo fits in.
	MaxImagePixels int64
}

// Default limits.
const (
	defaultMaxDepth       = 100
	defaultMaxStreamSize  = 256 << 20
	defaultMaxDecoded     = 2 << 30
	defaultMaxImagePixels = 50_000_000
)

func (l Limits) check() error {
	if l.MaxDepth < 0 || l.MaxStreamSize < 0 || l.MaxDecoded < 0 || l.MaxImagePixels < 0 {
		return &OptionError{"Limits", "limits cannot be negative"}
	}
	return nil
}

func (l Limits) withDefaults() Limits {
	if l.MaxDepth == 0 {
		l.MaxDepth = defaultMaxDepth
	}
	if l.MaxStreamSize == 0 {
		l.MaxStreamSize = defaultMaxStreamSize
	}
	if l.MaxDecoded == 0 {
		l.MaxDecoded = defaultMaxDecoded
	}
	if l.MaxImagePixels == 0 {
		l.MaxImagePixels = defaultMaxImagePixels
	}
	return l
}
