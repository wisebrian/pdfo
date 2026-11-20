package crypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rc4"
	"errors"
	"testing"

	"github.com/wisebrian/pdfo/internal/syntax"
)

// TestBodyMatchesStream checks that decrypting while writing gives exactly
// what decrypting at once gives, for every padding length.
func TestBodyMatchesStream(t *testing.T) {
	for _, m := range []Method{RC4, AESV2, AESV3} {
		h := &Handler{key: bytes.Repeat([]byte{7}, map[Method]int{RC4: 16, AESV2: 16, AESV3: 32}[m])}
		ref := syntax.Ref{Num: 12, Gen: 0}
		for _, n := range []int{0, 1, 15, 16, 17, 100000, 65536 + 3} {
			plain := bytes.Repeat([]byte("pdfo"), n/4+1)[:n]
			key := h.objectKey(ref, m)
			var enc []byte
			if m == RC4 {
				c, _ := rc4.NewCipher(key)
				enc = make([]byte, n)
				c.XORKeyStream(enc, plain)
			} else {
				p := 16 - n%16
				padded := append(append([]byte(nil), plain...), bytes.Repeat([]byte{byte(p)}, p)...)
				iv := bytes.Repeat([]byte{9}, 16)
				block, _ := aes.NewCipher(key)
				enc = make([]byte, len(padded))
				cipher.NewCBCEncrypter(block, iv).CryptBlocks(enc, padded)
				enc = append(iv, enc...)
			}
			whole, err := h.Stream(ref, m, enc)
			if err != nil || !bytes.Equal(whole, plain) {
				t.Errorf("method %d, %d bytes: Stream gave %d bytes, %v", m, n, len(whole), err)
				continue
			}
			b, err := h.Body(ref, m, enc)
			if err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if _, err := b.WriteTo(&buf); err != nil || b.Len() != int64(n) || !bytes.Equal(buf.Bytes(), plain) {
				t.Errorf("method %d, %d bytes: Body gave %d (Len %d), %v", m, n, buf.Len(), b.Len(), err)
			}
		}
	}
}

func TestUnsupported(t *testing.T) {
	pub := new(syntax.Dict)
	pub.Set("Filter", syntax.Name("Adobe.PubSec"))
	if _, err := New(pub, nil, ""); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("public-key handler: %v", err)
	}
	v3 := new(syntax.Dict)
	v3.Set("Filter", syntax.Name("Standard"))
	v3.Set("V", syntax.Int(3))
	if _, err := New(v3, nil, ""); !errors.Is(err, errors.ErrUnsupported) {
		t.Errorf("V 3: %v", err)
	}
}

func TestLenientAES(t *testing.T) {
	// Data that is not a whole number of blocks loses the partial block;
	// bad padding is left in place, as pdf.js does.
	h := &Handler{key: bytes.Repeat([]byte{1}, 16)}
	ref := syntax.Ref{Num: 1}
	out, err := h.Stream(ref, AESV2, bytes.Repeat([]byte{3}, 16+32+5))
	if err != nil || len(out) == 0 || len(out) > 32 {
		t.Errorf("odd length: %d bytes, %v", len(out), err)
	}
	if out, err := h.Stream(ref, AESV2, make([]byte, 16)); err != nil || len(out) != 0 {
		t.Errorf("IV only: %q, %v", out, err)
	}
}
