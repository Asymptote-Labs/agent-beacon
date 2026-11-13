//go:build !unix

package localingest

// openNoFollow and openNonBlock have no open(2) flag here; openSpoolFile's regular-file check still
// applies.
const (
	openNoFollow = 0
	openNonBlock = 0
)
