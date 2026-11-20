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
// # Reading
//
// Open reads PDF 1.0 to 2.0: cross-reference tables and streams, object
// streams, hybrid files and incremental updates. When the cross-reference
// data is missing or its offsets are wrong, it is rebuilt by scanning the
// file, as Acrobat and pdf.js do; Info().Repaired then says so. Encrypted
// files open with the standard security handler, revisions 2 to 6 (RC4,
// AES-128, AES-256); a file whose user password is empty opens without
// asking, others return ErrPasswordRequired until the password is given.
// Output is never encrypted; Report.InputEncrypted says the input was.
//
// Hostile files are bounded by Limits: nesting depth, decoded size per
// stream and in total, and every loop in references, page trees, outline
// trees and cross-reference chains is detected.
//
// # Page actions
//
// Every page action is Assemble: a new document made from a list of
// pages, each from a source document, with an optional extra rotation and
// resize. Merge, Split, SplitEvery, ExtractPages, DeletePages,
// ReorderPages, RotatePages and ResizePages build that list. Only the
// objects the pages use are copied, each shared one (a font, an image) once
// per source, and stream data is copied byte for byte.
//
// Pages keep everything visible: content, resources, annotations. Inherited
// attributes (MediaBox, CropBox, Resources, Rotate) are written on every
// page. Resizing scales a page's content uniformly, centred, and clips it to
// the old visible area so content hidden outside it stays hidden;
// annotations and link targets move with it.
//
// What happens to the rest of a document depends on the list of pages:
//
//   - The same pages, in order (rotating or resizing pages, a document
//     merged with nothing): everything is kept, including tags (the
//     structure tree), page labels and article threads.
//   - Pages of one document (extracting, deleting, reordering, splitting):
//     bookmarks are kept when their page is (a bookmark whose page is gone
//     stays as a heading if it has bookmarks below it that remain); named
//     destinations to kept pages, attachments, document scripts and the
//     opening action are kept. Tags, page labels and threads are dropped:
//     they describe the old page sequence.
//   - Pages of several documents (merging): each document's bookmarks
//     follow the previous one's; attachments are combined, renamed "name
//     (2)" when names collide; layers (optional content) are combined, so
//     hidden layers stay hidden. Document scripts and opening actions are
//     dropped: they were written for one document. Information and settings
//     such as the language come from the first document.
//
// In every case links to pages in the output go to them (named
// destinations become explicit), and links to pages that are gone become
// inert: the annotation stays, so nothing visible changes, but it no longer
// goes anywhere.
//
// A form stays interactive when its document is the only one in the output
// with a form and none of its pages appears twice; its field tree is pruned
// to the fields whose widgets remain. Otherwise its widgets are flattened:
// their appearances are drawn into the page, so filled-in values stay
// visible. Forms that leave drawing to the viewer (NeedAppearances), whose
// stored appearances are often empty, get appearances made from their
// values: text in the standard Helvetica, which covers Latin text only
// (other letters lose their accents or become "?"), and check marks.
// Digital signatures never survive: any change invalidates them,
// so signature widgets are always flattened (their appearance stays) and
// the signature removed. XFA forms are dropped; their standard fields
// remain.
//
// Every dropped item is reported in Report.Warnings, with a stable code:
// bookmarks-dropped, links-inert, form-flattened, form-value-not-visible,
// signature-removed, xfa-dropped, javascript-dropped, tags-dropped,
// page-labels-dropped, articles-dropped.
//
// # Compression
//
// Compress makes a document smaller without touching what does not need
// it. Each image's effective resolution is worked out from its pixel size
// and the size it is drawn at, by following the transformation matrix
// through the page's content, its form XObjects and its annotations'
// appearances down to the operator that draws it. Images drawn at more than
// 1.5 times the target resolution are downsampled with an area-averaging
// filter, their soft masks with them; JPEG images, and lossless ones that
// come out smaller as JPEG, are re-encoded at the requested quality. An
// image is replaced only when the result is at least 2% smaller. Identical
// streams are stored once, Flate streams are recompressed at the best
// level, uncompressed streams are compressed, unused objects disappear, and
// the file is written with object streams. The Screen, Ebook and Print
// presets set the usual targets; Report.Compression lists what happened to
// every image, and why when nothing did.
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
