// Package crypt implements the standard security handler of ISO 32000-2,
// section 7.6.4, for reading: revisions 2 to 6, with RC4 (40 to 128 bits),
// AES-128 and AES-256. Files are only ever decrypted; output is written
// unencrypted.
package crypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rc4"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
	"hash"
	"io"
	"strconv"
	"unicode/utf8"

	"github.com/wisebrian/pdfo/internal/syntax"
)

// ErrPassword means the password given is neither the user nor the owner
// password. The caller tells a missing password from a wrong one.
var ErrPassword = errors.New("incorrect password")

// Unsupported marks encryption this package cannot read; it matches
// errors.ErrUnsupported.
type Unsupported string

func (e Unsupported) Error() string        { return string(e) + " is not supported" }
func (e Unsupported) Is(target error) bool { return target == errors.ErrUnsupported }

// Method is how a crypt filter encrypts.
type Method int

// Methods.
const (
	None  Method = iota // Identity: not encrypted
	RC4                 // V2
	AESV2               // AES-128
	AESV3               // AES-256
)

// Handler decrypts the strings and streams of one file.
type Handler struct {
	key      []byte
	rev      int
	str, stm Method
	eff      Method // embedded files, which may use their own crypt filter
	hasEFF   bool
	filters  map[syntax.Name]Method
	// EncryptMetadata is false when the XMP metadata stream was left in
	// clear text, so it must not be decrypted.
	EncryptMetadata bool
	// Description names the encryption for display, such as "AES-256".
	Description string
}

var padding = []byte{
	0x28, 0xBF, 0x4E, 0x5E, 0x4E, 0x75, 0x8A, 0x41, 0x64, 0x00, 0x4E, 0x56, 0xFF, 0xFA, 0x01, 0x08,
	0x2E, 0x2E, 0x00, 0xB6, 0xD0, 0x68, 0x3E, 0x80, 0x2F, 0x0C, 0xA9, 0xFE, 0x64, 0x53, 0x69, 0x7A,
}

// New opens the encryption dictionary enc of a file whose first identifier
// is id, trying password first as the user password and then as the owner
// password. Most encrypted files have an empty user password; they open
// with "".
func New(enc *syntax.Dict, id []byte, password string) (*Handler, error) {
	if f, _ := enc.Get("Filter").(syntax.Name); f != "Standard" {
		if f == "Adobe.PubSec" || enc.Get("SubFilter") != nil {
			return nil, Unsupported("public-key encryption")
		}
		return nil, Unsupported("the " + strconv.Quote(string(f)) + " security handler")
	}
	v := intOf(enc.Get("V"), 0)
	r := intOf(enc.Get("R"), 0)
	h := &Handler{rev: r, EncryptMetadata: true, filters: map[syntax.Name]Method{}}
	if b, ok := enc.Get("EncryptMetadata").(syntax.Bool); ok {
		h.EncryptMetadata = bool(b)
	}
	o, _ := enc.Get("O").(syntax.String)
	u, _ := enc.Get("U").(syntax.String)
	p := uint32(int32(intOf(enc.Get("P"), 0)))
	keyLen := 5
	switch v {
	case 1:
		h.str, h.stm = RC4, RC4
	case 2:
		keyLen = intOf(enc.Get("Length"), 40) / 8
		h.str, h.stm = RC4, RC4
	case 4, 5:
		keyLen = 16
		if v == 5 {
			keyLen = 32
		}
		cf, _ := enc.Get("CF").(*syntax.Dict)
		for i := range cf.Len() {
			name, d := cf.At(i)
			fd, _ := d.(*syntax.Dict)
			switch cfm, _ := fd.Get("CFM").(syntax.Name); cfm {
			case "V2":
				h.filters[name] = RC4
				if l := intOf(fd.Get("Length"), 0); v == 4 && l >= 5 && l <= 16 {
					keyLen = l // bytes, in crypt filter dictionaries
				} else if l >= 40 && l <= 128 && v == 4 {
					keyLen = l / 8
				}
			case "AESV2":
				h.filters[name] = AESV2
			case "AESV3":
				h.filters[name] = AESV3
			case "None", "":
				h.filters[name] = None
			default:
				return nil, Unsupported("crypt filter method " + strconv.Quote(string(cfm)))
			}
		}
		h.filters["Identity"] = None
		var err error
		if h.stm, err = h.filter(enc.Get("StmF")); err != nil {
			return nil, err
		}
		if h.str, err = h.filter(enc.Get("StrF")); err != nil {
			return nil, err
		}
		if enc.Get("EFF") != nil {
			if h.eff, err = h.filter(enc.Get("EFF")); err != nil {
				return nil, err
			}
			h.hasEFF = true
		}
	default:
		return nil, Unsupported("encryption version " + strconv.Itoa(v))
	}
	if keyLen < 5 || keyLen > 32 {
		return nil, errors.New("encryption key length " + strconv.Itoa(keyLen*8) + " bits is invalid")
	}

	var err error
	switch r {
	case 2, 3, 4:
		if len(o) < 32 || len(u) < 32 {
			return nil, errors.New("encryption dictionary lacks O or U")
		}
		h.key, err = legacyKey(r, keyLen, []byte(password), []byte(o[:32]), []byte(u[:32]), p, id, h.EncryptMetadata)
	case 5, 6:
		oe, _ := enc.Get("OE").(syntax.String)
		ue, _ := enc.Get("UE").(syntax.String)
		if len(o) < 48 || len(u) < 48 || len(oe) < 32 || len(ue) < 32 {
			return nil, errors.New("encryption dictionary lacks O, U, OE or UE")
		}
		h.key, err = aes256Key(r, []byte(password), []byte(o), []byte(u), []byte(oe), []byte(ue))
	default:
		return nil, Unsupported("security handler revision " + strconv.Itoa(r))
	}
	if err != nil {
		return nil, err
	}
	h.Description = h.describe()
	return h, nil
}

func (h *Handler) filter(o syntax.Object) (Method, error) {
	name, _ := o.(syntax.Name)
	if name == "" {
		return None, nil // the default is Identity
	}
	m, ok := h.filters[name]
	if !ok {
		return None, errors.New("crypt filter " + strconv.Quote(string(name)) + " is not defined")
	}
	return m, nil
}

func (h *Handler) describe() string {
	switch max(h.stm, h.str) {
	case AESV3:
		return "AES-256"
	case AESV2:
		return "AES-128"
	case RC4:
		return "RC4 " + strconv.Itoa(len(h.key)*8) + "-bit"
	}
	return "none"
}

func intOf(o syntax.Object, def int) int {
	switch v := o.(type) {
	case syntax.Int:
		return int(v)
	case syntax.Real:
		return int(v)
	}
	return def
}

// legacyKey computes and checks the file key of revisions 2 to 4
// (algorithms 2, 6 and 7).
func legacyKey(r, n int, password, o, u []byte, p uint32, id []byte, encMeta bool) ([]byte, error) {
	pw := latin1(password)
	if key := userKey(r, n, pw, o, p, id, encMeta); checkUser(r, key, u, id) {
		return key, nil
	}
	// The owner password decrypts O into the user password (algorithm 7).
	sum := md5.Sum(pad(pw))
	k := sum[:]
	if r >= 3 {
		for range 50 {
			s := md5.Sum(k[:n])
			k = s[:]
		}
	}
	k = k[:n]
	user := append([]byte(nil), o...)
	if r == 2 {
		rc4XOR(k, user)
	} else {
		for i := 19; i >= 0; i-- {
			rc4XOR(xorKey(k, byte(i)), user)
		}
	}
	if key := userKey(r, n, user, o, p, id, encMeta); checkUser(r, key, u, id) {
		return key, nil
	}
	return nil, ErrPassword
}

// userKey is algorithm 2: the file key from a user password.
func userKey(r, n int, pw, o []byte, p uint32, id []byte, encMeta bool) []byte {
	m := md5.New()
	m.Write(pad(pw))
	m.Write(o)
	var pb [4]byte
	binary.LittleEndian.PutUint32(pb[:], p)
	m.Write(pb[:])
	m.Write(id)
	if r >= 4 && !encMeta {
		m.Write([]byte{0xFF, 0xFF, 0xFF, 0xFF})
	}
	k := m.Sum(nil)
	if r >= 3 {
		for range 50 {
			s := md5.Sum(k[:n])
			k = s[:]
		}
	}
	return k[:n]
}

// checkUser is algorithms 4 and 5: whether key reproduces the U entry.
func checkUser(r int, key, u, id []byte) bool {
	if r == 2 {
		x := append([]byte(nil), padding...)
		rc4XOR(key, x)
		return bytes.Equal(x, u[:32])
	}
	m := md5.New()
	m.Write(padding)
	m.Write(id)
	x := m.Sum(nil)
	rc4XOR(key, x)
	for i := 1; i <= 19; i++ {
		rc4XOR(xorKey(key, byte(i)), x)
	}
	return bytes.Equal(x, u[:16])
}

func pad(pw []byte) []byte {
	out := make([]byte, 32)
	n := copy(out, pw)
	copy(out[n:], padding)
	return out
}

func xorKey(k []byte, b byte) []byte {
	out := make([]byte, len(k))
	for i := range k {
		out[i] = k[i] ^ b
	}
	return out
}

func rc4XOR(key, data []byte) {
	c, err := rc4.NewCipher(key)
	if err != nil {
		return // keys here are 5 to 16 bytes; rc4 accepts 1 to 256
	}
	c.XORKeyStream(data, data)
}

// latin1 converts a password typed as UTF-8 to the PDFDocEncoding that
// revisions 2 to 4 hash, which agrees with Latin-1 for the characters that
// can be typed; others are dropped, as Acrobat does.
func latin1(pw []byte) []byte {
	if utf8.Valid(pw) {
		out := make([]byte, 0, len(pw))
		for _, r := range string(pw) {
			if r < 256 {
				out = append(out, byte(r))
			}
		}
		return out
	}
	return pw
}

// aes256Key computes the file key of revisions 5 and 6 (algorithm 2.A).
func aes256Key(r int, password, o, u, oe, ue []byte) ([]byte, error) {
	pw := password
	if len(pw) > 127 {
		pw = pw[:127]
	}
	if hashR(r, pw, u[32:40], nil, u[:32]) {
		return unwrapKey(hash2B(r, pw, u[40:48], nil), ue[:32])
	}
	if hashR(r, pw, o[32:40], u[:48], o[:32]) {
		return unwrapKey(hash2B(r, pw, o[40:48], u[:48]), oe[:32])
	}
	return nil, ErrPassword
}

func hashR(r int, pw, salt, udata, want []byte) bool {
	return bytes.Equal(hash2B(r, pw, salt, udata), want)
}

// hash2B is the password hash: SHA-256 for revision 5, the iterated hash of
// algorithm 2.B for revision 6.
func hash2B(r int, pw, salt, udata []byte) []byte {
	h := sha256.New()
	h.Write(pw)
	h.Write(salt)
	h.Write(udata)
	k := h.Sum(nil)
	if r == 5 {
		return k
	}
	for round := 0; ; round++ {
		unit := make([]byte, 0, len(pw)+len(k)+len(udata))
		unit = append(append(append(unit, pw...), k...), udata...)
		k1 := bytes.Repeat(unit, 64)
		block, _ := aes.NewCipher(k[:16])
		e := make([]byte, len(k1))
		cipher.NewCBCEncrypter(block, k[16:32]).CryptBlocks(e, k1)
		var sum int
		for _, b := range e[:16] {
			sum += int(b)
		}
		var hh hash.Hash
		switch sum % 3 {
		case 0:
			hh = sha256.New()
		case 1:
			hh = sha512.New384()
		default:
			hh = sha512.New()
		}
		hh.Write(e)
		k = hh.Sum(nil)
		if round >= 63 && int(e[len(e)-1]) <= round+1-32 {
			break
		}
	}
	return k[:32]
}

// unwrapKey decrypts the file key from OE or UE: AES-256 with no IV and no
// padding.
func unwrapKey(kek, wrapped []byte) ([]byte, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 32)
	cipher.NewCBCDecrypter(block, make([]byte, 16)).CryptBlocks(key, wrapped[:32])
	return key, nil
}

// objectKey derives the key for one object (algorithm 1); AES-256 uses the
// file key itself.
func (h *Handler) objectKey(ref syntax.Ref, m Method) []byte {
	if m == AESV3 {
		return h.key
	}
	b := make([]byte, 0, len(h.key)+9)
	b = append(b, h.key...)
	b = append(b, byte(ref.Num), byte(ref.Num>>8), byte(ref.Num>>16), byte(ref.Gen), byte(ref.Gen>>8))
	if m == AESV2 {
		b = append(b, "sAlT"...)
	}
	sum := md5.Sum(b)
	return sum[:min(len(h.key)+5, 16)]
}

// String decrypts a string of the object ref.
func (h *Handler) String(ref syntax.Ref, s syntax.String) syntax.String {
	out, _ := h.decrypt(ref, h.str, []byte(s))
	return syntax.String(out)
}

// StreamMethod returns how the stream with dictionary d is encrypted: by
// the crypt filter it names, or by the file's default for streams.
// Cross-reference streams are never encrypted, nor is the metadata stream
// when the file says so.
func (h *Handler) StreamMethod(d *syntax.Dict) Method {
	def := h.stm
	switch t, _ := d.Get("Type").(syntax.Name); {
	case t == "XRef":
		return None
	case t == "Metadata" && !h.EncryptMetadata:
		return None
	case t == "EmbeddedFile" && h.hasEFF:
		def = h.eff
	}
	filters := d.Get("Filter")
	parms := d.Get("DecodeParms")
	if f, ok := filters.(syntax.Name); ok {
		filters = syntax.Array{f}
		parms = syntax.Array{parms}
	}
	fa, _ := filters.(syntax.Array)
	pa, _ := parms.(syntax.Array)
	for i, f := range fa {
		if f == syntax.Name("Crypt") {
			var p *syntax.Dict
			if i < len(pa) {
				p, _ = pa[i].(*syntax.Dict)
			}
			name, _ := p.Get("Name").(syntax.Name)
			if name == "" {
				name = "Identity"
			}
			if m, ok := h.filters[name]; ok {
				return m
			}
			return def
		}
	}
	return def
}

// Stream decrypts stream data of the object ref with method m.
func (h *Handler) Stream(ref syntax.Ref, m Method, data []byte) ([]byte, error) {
	return h.decrypt(ref, m, data)
}

func (h *Handler) decrypt(ref syntax.Ref, m Method, data []byte) ([]byte, error) {
	switch m {
	case None:
		return data, nil
	case RC4:
		out := append([]byte(nil), data...)
		rc4XOR(h.objectKey(ref, m), out)
		return out, nil
	}
	return aesDecrypt(h.objectKey(ref, m), data)
}

// aesDecrypt decrypts AES-CBC data whose first 16 bytes are the IV and whose
// last block carries PKCS#5 padding. Like pdf.js it is lenient: a length
// that is not a whole number of blocks loses the partial block, and bad
// padding is left in place.
func aesDecrypt(key, data []byte) ([]byte, error) {
	if len(data) < 32 {
		if len(data) == 16 {
			return nil, nil // an IV alone: the empty string
		}
		return nil, errors.New("AES data too short")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	n := (len(data) - 16) / 16 * 16
	out := make([]byte, n)
	cipher.NewCBCDecrypter(block, data[:16]).CryptBlocks(out, data[16:16+n])
	return unpad(out), nil
}

func unpad(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	p := int(b[len(b)-1])
	if p == 0 || p > 16 || p > len(b) {
		return b
	}
	for _, c := range b[len(b)-p:] {
		if int(c) != p {
			return b
		}
	}
	return b[:len(b)-p]
}

// Body returns stream data that decrypts as it is written, so a file being
// rewritten never holds a decrypted copy of every stream at once. Its
// length is known in advance: RC4 keeps it, and the padding of AES is read
// from the last block alone.
func (h *Handler) Body(ref syntax.Ref, m Method, data []byte) (Body, error) {
	key := h.objectKey(ref, m)
	switch m {
	case None:
		return &plainBody{data}, nil
	case RC4:
		return &rc4Body{key: key, data: data}, nil
	}
	if len(data) < 32 || len(data)%16 != 0 {
		// Unusual lengths: decrypt now, leniently, rather than in pieces.
		out, err := aesDecrypt(key, data)
		if err != nil {
			return nil, err
		}
		return &plainBody{out}, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	last := make([]byte, 16)
	cipher.NewCBCDecrypter(block, data[len(data)-32:len(data)-16]).CryptBlocks(last, data[len(data)-16:])
	trim := 16 - len(unpad(last))
	return &aesBody{block: block, data: data, length: int64(len(data) - 16 - trim)}, nil
}

// Body is stream data written by WriteTo, Len bytes long.
type Body interface {
	Len() int64
	WriteTo(w io.Writer) (int64, error)
}

type plainBody struct{ data []byte }

func (b *plainBody) Len() int64 { return int64(len(b.data)) }
func (b *plainBody) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(b.data)
	return int64(n), err
}

const chunk = 64 << 10

type rc4Body struct {
	key  []byte
	data []byte
}

func (b *rc4Body) Len() int64 { return int64(len(b.data)) }
func (b *rc4Body) WriteTo(w io.Writer) (int64, error) {
	c, err := rc4.NewCipher(b.key)
	if err != nil {
		return 0, err
	}
	buf := make([]byte, min(chunk, len(b.data)))
	var total int64
	for off := 0; off < len(b.data); off += chunk {
		in := b.data[off:min(off+chunk, len(b.data))]
		c.XORKeyStream(buf[:len(in)], in)
		n, err := w.Write(buf[:len(in)])
		total += int64(n)
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

type aesBody struct {
	block  cipher.Block
	data   []byte
	length int64
}

func (b *aesBody) Len() int64 { return b.length }
func (b *aesBody) WriteTo(w io.Writer) (int64, error) {
	dec := cipher.NewCBCDecrypter(b.block, b.data[:16])
	src := b.data[16:]
	buf := make([]byte, min(chunk, len(src)))
	var total int64
	for off := 0; off < len(src) && total < b.length; off += chunk {
		in := src[off:min(off+chunk, len(src))]
		dec.CryptBlocks(buf[:len(in)], in)
		out := buf[:min(int64(len(in)), b.length-total)]
		n, err := w.Write(out)
		total += int64(n)
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
