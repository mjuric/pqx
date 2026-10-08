package fmtx

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mjuric/pqx/go/internal/data"
)

// starterFormat is the prototype's cell text; WP3 replaces it with
// format_value.
func starterFormat(v data.Value) string {
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
		return v
	case []byte:
		n := min(len(v), 16)
		more := ""
		if len(v) > 16 {
			more = "…"
		}
		return "0x" + hex.EncodeToString(v[:n]) + more + " (" + strconv.Itoa(len(v)) + " B)"
	case data.Timestamp:
		s := v.T.UTC().Format(time.DateTime)
		if ns := v.T.Nanosecond(); ns != 0 {
			s += strings.TrimRight(fmt.Sprintf(".%09d", ns), "0")
		}
		if v.Zoned {
			s += "Z"
		}
		return s
	case data.Date:
		return v.Time().Format(time.DateOnly)
	}
	return fmt.Sprint(v)
}
