package data

import (
	"context"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Null is the prototype's cell text for NULL; these tests compare cells as
// the prototype's text (cellText), which keeps them checking what they did
// before values were typed.
const Null = "∅"

// cellText is the prototype's FormatCell for a Value: integers as is, floats
// with 6 significant digits, timestamps in UTC as Python's isoformat (a Z if
// zoned), binary as hex, text sanitized.
func cellText(v Value) string {
	switch v := v.(type) {
	case nil:
		return Null
	case int64:
		return strconv.FormatInt(v, 10)
	case uint64:
		return strconv.FormatUint(v, 10)
	case float32:
		return strconv.FormatFloat(float64(v), 'g', 6, 32)
	case float64:
		return strconv.FormatFloat(v, 'g', 6, 64)
	case bool:
		return strconv.FormatBool(v)
	case string:
		return Sanitize(v)
	case []byte:
		n := min(len(v), 16)
		more := ""
		if len(v) > 16 {
			more = "…"
		}
		return "0x" + hex.EncodeToString(v[:n]) + more + " (" + strconv.Itoa(len(v)) + " B)"
	case Timestamp:
		s := v.T.UTC().Format(time.DateTime) + fraction(v.T.Nanosecond())
		if v.Zoned {
			s += "Z"
		}
		return s
	case Date:
		return v.Time().Format(time.DateOnly)
	case UUID:
		h := hex.EncodeToString(v[:])
		return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
	case TimeOfDay:
		t := time.Unix(0, int64(v)).UTC()
		return t.Format(time.TimeOnly) + fraction(t.Nanosecond())
	}
	return Sanitize(fmt.Sprint(v))
}

func fraction(ns int) string {
	switch {
	case ns == 0:
		return ""
	case ns%1000 == 0:
		return "." + pad(ns/1000, 6)
	default:
		return "." + pad(ns, 9)
	}
}

func pad(v, width int) string {
	s := strconv.Itoa(v)
	return strings.Repeat("0", width-len(s)) + s
}

// textCols is w.Cols as cellText.
func textCols(w Window) map[string][]string {
	out := make(map[string][]string, len(w.Cols))
	for c, vals := range w.Cols {
		t := make([]string, len(vals))
		for i, v := range vals {
			t[i] = cellText(v)
		}
		out[c] = t
	}
	return out
}

// checkWhere is the prototype's CheckWhere through Validate.
func checkWhere(ds Dataset, where string) error {
	_, err := ds.Validate(context.Background(), View{Where: where})
	return err
}
