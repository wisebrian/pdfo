// Package syntax holds the PDF object model and its canonical serialization
// (ISO 32000-2, section 7.3). The reader and the writer share it, so an object
// read from one file can be written to another without conversion.
package syntax

// Object is a PDF object. A nil Object is the null object: the specification
// treats a dictionary entry whose value is null as absent, and representing
// null as nil makes that equivalence free.
type Object interface{ isObject() }

// Bool is a boolean object.
type Bool bool

// Int is an integer object. It is 64 bits wide because offsets and lengths in
// large files exceed the 32-bit range the specification recommends for
// integers, and a reader must still represent them to reject or repair them.
type Int int64

// Real is a real-number object.
type Real float64

// Name is a name object, without the leading slash and with #xx escapes
// already decoded, so names compare equal however they were spelled.
type Name string

// String is a string object holding raw bytes. Whether it was written as a
// literal or a hexadecimal string carries no meaning, so it is not recorded;
// the writer chooses the form.
type String string

// Array is an array object.
type Array []Object

// Ref is an indirect reference.
type Ref struct {
	Num uint32 // object number
	Gen uint16 // generation number
}

// Stream is a stream object. Data holds the stream's bytes as stored, still
// encoded by the filters its dictionary names. A stream can only be an
// indirect object; it never appears inside another object.
type Stream struct {
	Dict *Dict  // the stream dictionary
	Data []byte // the stored bytes, still encoded
}

func (Bool) isObject()    {}
func (Int) isObject()     {}
func (Real) isObject()    {}
func (Name) isObject()    {}
func (String) isObject()  {}
func (Array) isObject()   {}
func (Ref) isObject()     {}
func (*Dict) isObject()   {}
func (*Stream) isObject() {}

// IsZero reports whether r is the zero Ref, which no file uses: object number
// 0 is always the head of the free list.
func (r Ref) IsZero() bool { return r == Ref{} }

// Dict is a dictionary that keeps its keys in insertion order. Order means
// nothing in PDF, but writing keys in a fixed order is what makes output
// deterministic; a Go map would iterate differently on every run.
type Dict struct {
	keys []Name
	vals []Object
}

// Len returns the number of entries.
func (d *Dict) Len() int {
	if d == nil {
		return 0
	}
	return len(d.keys)
}

// At returns the i-th entry, for iterating in order without allocating.
func (d *Dict) At(i int) (Name, Object) { return d.keys[i], d.vals[i] }

// Get returns the value for k, or nil when k is absent. Lookup is linear:
// dictionaries in real files are small, and a slice keeps their order
// without the cost of an index.
func (d *Dict) Get(k Name) Object {
	if d == nil {
		return nil
	}
	for i, key := range d.keys {
		if key == k {
			return d.vals[i]
		}
	}
	return nil
}

// Set sets k to v, keeping k's position if it is already present. Setting nil
// deletes k, since a null value and an absent key mean the same thing.
func (d *Dict) Set(k Name, v Object) {
	for i, key := range d.keys {
		if key == k {
			if v == nil {
				d.delete(i)
			} else {
				d.vals[i] = v
			}
			return
		}
	}
	if v != nil {
		d.keys = append(d.keys, k)
		d.vals = append(d.vals, v)
	}
}

// Delete removes k if it is present.
func (d *Dict) Delete(k Name) {
	for i, key := range d.keys {
		if key == k {
			d.delete(i)
			return
		}
	}
}

func (d *Dict) delete(i int) {
	d.keys = append(d.keys[:i], d.keys[i+1:]...)
	d.vals = append(d.vals[:i], d.vals[i+1:]...)
}

// Clone returns a shallow copy, so a caller can change entries without
// affecting a dictionary that other objects still share.
func (d *Dict) Clone() *Dict {
	if d == nil {
		return &Dict{}
	}
	return &Dict{
		keys: append([]Name(nil), d.keys...),
		vals: append([]Object(nil), d.vals...),
	}
}
