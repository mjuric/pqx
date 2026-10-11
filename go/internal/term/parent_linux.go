package term

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
)

// parentOf is pid's parent, from /proc.
func parentOf(pid int) (int, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	f := bytes.Fields(b[bytes.LastIndexByte(b, ')')+1:])
	if len(f) < 2 {
		return 0, fmt.Errorf("unexpected /proc/%d/stat", pid)
	}
	return strconv.Atoi(string(f[1]))
}
