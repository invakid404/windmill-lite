package syncfolder

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"unicode/utf8"
)

// The CLI reads every text file of the tree through one decoder (SY-19): a
// leading UTF-8 byte order mark is removed, a leading UTF-16 or UTF-32 one
// is refused, and anything else is UTF-8. What a refusal does depends on
// the caller; each reader below says.

var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// bomEncoding names the encoding of a leading UTF-16 or UTF-32 byte order
// mark, or returns "". `FF FE 00 00` is UTF-32 LE, not UTF-16 LE.
func bomEncoding(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE, 0, 0}):
		return "UTF-32 LE"
	case bytes.HasPrefix(data, []byte{0, 0, 0xFE, 0xFF}):
		return "UTF-32 BE"
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}):
		return "UTF-16 LE"
	case bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return "UTF-16 BE"
	}
	return ""
}

// refusal is the decoder's error for a file with a UTF-16 or UTF-32 byte
// order mark, in the CLI's words.
func refusal(file, encoding string) *Error {
	return &Error{Kind: ErrText, File: file,
		Msg: fmt.Sprintf("File %s is encoded as %s, which is not supported. Please convert it to UTF-8.", file, encoding)}
}

// readDecoded reads a text file of the folder through the decoder. found
// is false when the file does not exist or is a directory; refused is the
// decoder's refusal, which each caller handles as the CLI does.
func (f *Folder) readDecoded(p string) (data []byte, found bool, refused, err *Error) {
	data, found, rerr := f.readFile(p)
	if rerr != nil {
		return nil, false, nil, rerr.(*Error)
	}
	if !found {
		return nil, false, nil, nil
	}
	if enc := bomEncoding(data); enc != "" {
		return nil, true, refusal(p, enc), nil
	}
	return bytes.TrimPrefix(data, utf8BOM), true, nil, nil
}

// readReference reads a file reached only through a reference: a script or
// inline lock, or inline content. The CLI's readers there treat a file
// they cannot read, for any reason, the decoder's refusal included, as
// missing (SY-5, SY-7, SY-19); unreadable is that failure, for the
// caller's fallback and warning. A file the CLI's walk reads fails the push
// instead, and so does a failed read through a symbolic link, since reads
// never follow a link out of the root (ADR 0002).
func (f *Folder) readReference(p string) (data []byte, found bool, unreadable, err *Error) {
	data, found, refused, err := f.readDecoded(p)
	switch {
	case err == nil && refused == nil:
		return data, found, nil, nil
	case f.walked[p]:
	case refused != nil:
		return nil, false, refused, nil
	case errors.Is(err, fs.ErrPermission) || !f.throughLink(p):
		return nil, false, err, nil
	}
	if err == nil {
		err = refused
	}
	return nil, false, nil, err
}

// throughLink reports whether p, or a directory on the way to it, is a
// symbolic link.
func (f *Folder) throughLink(p string) bool {
	for q := p; q != "." && q != "/"; q = path.Dir(q) {
		if st, err := fs.Lstat(f.fsys, q); err == nil && st.Mode()&fs.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// checkText refuses a script, inline or lock file that is not valid UTF-8,
// where the CLI would read invalid sequences as U+FFFD (a restriction of
// 0001 §5a).
func checkText(data []byte, file, item string) (string, error) {
	if !utf8.Valid(data) {
		return "", &Error{Kind: ErrText, Item: item, File: file, Msg: "the file is not valid UTF-8"}
	}
	return string(data), nil
}

// sniff reports the decoder's refusal of a file the CLI's walk reads, from
// its first bytes. An unreadable file is not reported here: reading its
// item reports it.
func (f *Folder) sniff(p string) *Error {
	r, err := f.fsys.Open(p)
	if err != nil {
		return nil
	}
	defer r.Close()
	head := make([]byte, 4)
	n, _ := io.ReadFull(r, head)
	if enc := bomEncoding(head[:n]); enc != "" {
		return refusal(p, enc)
	}
	return nil
}
