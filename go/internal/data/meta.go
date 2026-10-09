package data

import (
	"strings"
	"sync"
	"unicode"

	"github.com/apache/arrow-go/v18/arrow"
)

// analysis is the state of the footer and analysis calls (WP2): the footer
// scan, cached; encodings per leaf path; the file's totals; and the view t
// that SQL views query.
type analysis struct {
	mu       sync.Mutex
	footer   *footerScan   // the finished scan, once there is one
	scanning chan struct{} // closed when the scan running now ends; nil if none runs
	enc      map[string][]string

	infoOnce sync.Once
	info     FileInfo

	tMu    sync.Mutex
	tReady bool
}

// unitDesc is a field's unit and description from its metadata (pqx's
// _column_info): "unit" or "units", and "description", "doc" or "comment"
// (the first that isn't empty); a felis-style description "[unit] text"
// gives both when there is no unit key.
func unitDesc(md arrow.Metadata) (unit, desc string) {
	get := func(k string) string {
		if i := md.FindKey(k); i >= 0 {
			return md.Values()[i]
		}
		return ""
	}
	for _, k := range []string{"description", "doc", "comment"} {
		if desc = get(k); desc != "" {
			break
		}
	}
	for _, k := range []string{"unit", "units"} {
		if unit = get(k); unit != "" {
			break
		}
	}
	if unit == "" {
		if u, rest, ok := felis(desc); ok {
			unit, desc = u, rest
		}
	}
	return unit, desc
}

// felis splits "[unit] text" as Python's re.match(r"^\s*\[([^\]]*)\]\s*(.*)$")
// does: . doesn't match a newline and $ matches at the end or before a final
// newline, so text of several lines doesn't match; \s is Python's (Unicode)
// whitespace.
func felis(s string) (unit, rest string, ok bool) {
	t := strings.TrimLeftFunc(s, pySpace)
	if !strings.HasPrefix(t, "[") {
		return "", "", false
	}
	end := strings.IndexByte(t, ']')
	if end < 0 {
		return "", "", false
	}
	unit = t[1:end]
	rest = strings.TrimLeftFunc(t[end+1:], pySpace)
	rest = strings.TrimSuffix(rest, "\n")
	if strings.Contains(rest, "\n") {
		return "", "", false
	}
	return unit, rest, true
}

// pySpace is Python's str.isspace (which re's \s follows for str patterns).
func pySpace(r rune) bool {
	return unicode.IsSpace(r) || r >= 0x1c && r <= 0x1f
}

// KeyValueMetadata is the file's key-value metadata in file order. As in pqx
// (a dict), a key that appears more than once is listed once, where it first
// appears, with its last value.
func (d *dataset) KeyValueMetadata() []KeyValue {
	kv := d.md.KeyValueMetadata()
	keys, vals := kv.Keys(), kv.Values()
	out := make([]KeyValue, 0, len(keys))
	at := make(map[string]int, len(keys))
	for i, k := range keys {
		if j, ok := at[k]; ok {
			out[j].Value = vals[i]
			continue
		}
		at[k] = len(out)
		out = append(out, KeyValue{Key: k, Value: vals[i]})
	}
	return out
}

// Info is what the footer says about the file. The sizes add up the column
// chunks' (compressed) and the row groups' (uncompressed) sizes, as pqx's
// overview does; they come from the parsed footer without decoding
// statistics, so they are cheap even for a million chunks.
func (d *dataset) Info() FileInfo {
	d.an.infoOnce.Do(func() {
		md := d.md
		fi := FileInfo{
			Path:          d.path,
			Size:          d.size,
			FooterSize:    int64(md.Size()),
			FormatVersion: "1.0",
			CreatedBy:     md.GetCreatedBy(),
			NumLeaves:     md.Schema.NumColumns(),
		}
		if md.FileMetaData.Version == 2 {
			fi.FormatVersion = "2.6" // what PyArrow (and arrow-go) take version 2 for
		}
		for _, rg := range md.RowGroups {
			fi.Uncompressed += rg.GetTotalByteSize()
			for _, c := range rg.Columns {
				if m := c.GetMetaData(); m != nil {
					fi.Compressed += m.GetTotalCompressedSize()
				}
			}
		}
		d.an.info = fi
	})
	return d.an.info
}
