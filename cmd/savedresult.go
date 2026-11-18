package cmd

import (
	"bytes"
	"crypto/sha256"
	"io"
	"math/rand/v2"
	"strconv"

	"github.com/nlink-jp/gem-agent/internal/bounded"
	"github.com/nlink-jp/gem-agent/internal/place"
)

// saveResult puts data under name in the session's work directory:
// in the directory the session took, through the descriptor it holds,
// by one name. The write lane writes that directory and can put a link
// under its name; a file made by path was then made wherever the link
// led (measured, ADR-0122). A work directory that is no longer the one
// the session took saves nothing, and the caller reports that.
//
// A file already under the name is kept only when it is a regular file
// — itself, no link to one — whose bytes are these: it is opened
// without waiting, and no more than the result's size is read. Anything
// else is replaced by a temporary file renamed over it, so a reader
// never sees half a result.
func saveResult(dir, name string, data []byte) error {
	work, err := place.Of(dir)
	if err != nil {
		return err
	}
	d := work.Dir()
	if sameAsSaved(d, name, data) {
		return nil
	}
	tmp := name + ".tmp-" + strconv.FormatUint(rand.Uint64(), 36)
	f, err := d.Create(tmp, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	// The close is checked: it is the last thing between the data and
	// durability, and a file that closed badly must never be renamed
	// into place looking complete.
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = d.Rename(tmp, name)
	}
	if err != nil {
		_ = d.Remove(tmp)
	}
	return err
}

// sameAsSaved reports whether name in d is a regular file holding
// exactly data.
func sameAsSaved(d *bounded.Dir, name string, data []byte) bool {
	f, err := d.OpenRegular(name)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, int64(len(data))+1))
	want := sha256.Sum256(data)
	return err == nil && n == int64(len(data)) && bytes.Equal(h.Sum(nil), want[:])
}
