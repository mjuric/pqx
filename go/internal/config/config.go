// Package config reads and writes ~/.config/pqx/formats.yaml (or under
// $XDG_CONFIG_HOME if that is absolute), the column formats remembered by
// column name. The file format, locking and atomic writes are Python pqx's
// (config.py), so both versions can share the file. Part of the contract
// (docs/design/go-port.md); WP3 implements it.
package config

import (
	"errors"

	"github.com/mjuric/pqx/go/internal/fmtx"
)

// ErrConfig wraps a formats.yaml that can't be read or parsed.
var ErrConfig = errors.New("formats.yaml")

// Path is the formats file's path.
func Path() string { return "" }

// LoadFormats reads the formats file at path ("" for Path()): column name
// to override, skipping invalid entries. A missing file is no formats and
// no error; an unreadable or unparsable one is an error wrapping ErrConfig.
func LoadFormats(path string) (map[string]fmtx.Override, error) { return nil, nil }

// SaveFormat sets (or, for the zero Override, removes) one column's format
// in the file at path ("" for Path()), under a lock, merging with what is
// there, and writing atomically. It returns the path written.
func SaveFormat(name string, o fmtx.Override, path string) (string, error) {
	return "", errors.New("saving formats: WP3")
}
