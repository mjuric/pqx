package term

import "golang.org/x/sys/unix"

// parentOf is pid's parent, from sysctl.
func parentOf(pid int) (int, error) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, err
	}
	return int(k.Eproc.Ppid), nil
}
