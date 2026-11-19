// Package pdfo turns images into PDFs, rearranges the pages of PDFs and makes
// them smaller, using only the Go standard library.
//
// Every function takes bytes and returns bytes; nothing touches files, the
// network or the clock. That is what lets the same code run in a browser tab
// as WebAssembly, where the user's files never leave their machine.
//
// Output is deterministic: the same input and options give the same bytes.
// The file identifier is a hash of the content, and dates are written only
// when the caller supplies one in WriteOptions.Timestamp.
//
// # Images to PDF
//
// FromImages makes one page per JPEG or PNG image:
//
//	out, err := pdfo.FromImages([][]byte{photo1, photo2}, &pdfo.ImageOptions{
//		PageSize: pdfo.A4,
//		Margin:   pdfo.MM(10),
//	})
//	if err != nil {
//		return err
//	}
//	pdf := out.Bytes()
//
// JPEGs are embedded without being decoded and re-encoded, so they keep
// their exact quality. What is removed is the metadata a viewer has no use
// for and a recipient should not get by accident: EXIF (which can hold the
// GPS position of a photo), XMP, comments, thumbnails and data after the end
// of the image, such as the video of a "motion photo". The EXIF orientation
// is applied by the page's drawing matrix instead, since PDF viewers ignore
// EXIF. PNGs are decoded and stored losslessly with Flate and PNG
// predictors; transparency becomes a soft mask.
//
// # Output
//
// An Output is a plan of the file's bytes. Size reports its exact length,
// and WriteTo writes it, copying large data straight from the inputs. Bytes
// returns it as a slice. Output.Report lists what the operation could not
// keep, as Warnings with stable codes for the UI to translate.
//
// # Errors
//
// Errors are typed so a UI can respond to them: OptionError for an option
// that cannot work, ImageError for an input image that was rejected (wrapping
// the reason), FormatError for damaged input, LimitError for input beyond
// the Limits. Features this package does not implement match
// errors.ErrUnsupported.
package pdfo
