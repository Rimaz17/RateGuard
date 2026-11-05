package config

import (
	"fmt"
	"io/fs"
	"strconv"
)

// FileMode is a file permission written in octal, such as 0660.
type FileMode fs.FileMode

// UnmarshalText implements encoding.TextUnmarshaler, which the environment
// parser uses. Only permission bits are accepted.
func (m *FileMode) UnmarshalText(text []byte) error {
	v, err := strconv.ParseUint(string(text), 8, 32)
	if err != nil || v > 0o777 {
		return fmt.Errorf("%q is not an octal permission such as 0660", text)
	}
	*m = FileMode(v)
	return nil
}

// Perm returns the mode as fs.FileMode.
func (m FileMode) Perm() fs.FileMode { return fs.FileMode(m) }
