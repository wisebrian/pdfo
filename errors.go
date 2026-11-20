package pdfo

import (
	"strconv"
	"strings"
)

// Features this package does not implement, such as an arithmetic-coded
// JPEG, are reported with errors that match errors.ErrUnsupported, so
// callers can tell "can't" from "broken".

// FormatError reports input that is not in the format it should be, or is
// damaged beyond what this package can repair.
type FormatError struct {
	// Offset is where in the input the problem was found, or -1 when it is
	// nowhere in particular.
	Offset int64
	// Msg says what is wrong, in English.
	Msg string
}

func (e *FormatError) Error() string {
	if e.Offset >= 0 {
		return "pdfo: " + e.Msg + " (at byte " + strconv.FormatInt(e.Offset, 10) + ")"
	}
	return "pdfo: " + e.Msg
}

// LimitError reports input that would exceed one of the Limits. It is its
// own type because the message to show differs: the file may be valid, just
// too large for a browser tab.
type LimitError struct {
	Limit string // which limit, e.g. "image size"
	Value int64  // the bound that was hit
}

func (e *LimitError) Error() string {
	return "pdfo: input exceeds the " + e.Limit + " limit of " + strconv.FormatInt(e.Value, 10)
}

// ImageError says which input FromImages rejected, so the UI can point at
// the file instead of failing the whole batch without a clue.
type ImageError struct {
	Index int // zero-based position in the input
	// Err is the reason: a *FormatError, a *LimitError, or an error
	// matching errors.ErrUnsupported.
	Err error
}

func (e *ImageError) Error() string {
	return "pdfo: image " + strconv.Itoa(e.Index+1) + ": " + strings.TrimPrefix(e.Err.Error(), "pdfo: ")
}

// Unwrap returns the reason the image was rejected.
func (e *ImageError) Unwrap() error { return e.Err }

// OptionError reports an option set to a value that cannot work, such as a
// negative margin. It is a separate type so the UI can tell its own mistakes
// from problems with the user's files.
type OptionError struct {
	Option string // the field, e.g. "Margin"
	// Msg says what the option must be instead, in English.
	Msg string
}

func (e *OptionError) Error() string { return "pdfo: " + e.Option + ": " + e.Msg }
