//go:build linux

package procobserver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func verifyActivationPeer(c *net.UnixConn) error {
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var peerErr error
	err = raw.Control(func(fd uintptr) { cred, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err != nil || peerErr != nil || cred == nil || cred.Uid != 0 || cred.Pid != 1 {
		return fmt.Errorf("observer socket is not the fixed root systemd activation peer")
	}
	return nil
}

// Socket activation preserves the listener creator's peer credentials. Enable
// kernel per-message credentials instead to authenticate the actual helper
// writing each response byte. write(fd3) needs no sendmsg/credential syscall.
func newWriterAuthenticatedReader(c *net.UnixConn, uid uint32) (func([]byte) (int, error), error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return nil, err
	}
	var optionErr error
	err = raw.Control(func(fd uintptr) { optionErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PASSCRED, 1) })
	if err != nil || optionErr != nil {
		return nil, fmt.Errorf("observer writer credential transport unavailable")
	}
	writerPID := int32(0)
	return func(data []byte) (int, error) {
		oob := make([]byte, unix.CmsgSpace(unix.SizeofUcred)+unix.CmsgSpace(32))
		n, on, flags, _, readErr := c.ReadMsgUnix(data, oob)
		messages, parseErr := unix.ParseSocketControlMessage(oob[:on])
		invalid := parseErr != nil || flags&(unix.MSG_CTRUNC|unix.MSG_TRUNC) != 0
		var sender *unix.Ucred
		for _, m := range messages {
			switch {
			case m.Header.Level == unix.SOL_SOCKET && m.Header.Type == unix.SCM_RIGHTS:
				fds, e := unix.ParseUnixRights(&m)
				if e == nil {
					for _, fd := range fds {
						_ = unix.Close(fd)
					}
				}
				invalid = true
			case m.Header.Level == unix.SOL_SOCKET && m.Header.Type == unix.SCM_CREDENTIALS:
				cred, e := unix.ParseUnixCredentials(&m)
				if e != nil || sender != nil {
					invalid = true
				} else {
					sender = cred
				}
			default:
				invalid = true
			}
		}
		if n > 0 {
			if sender == nil || sender.Uid != uid || sender.Pid <= 1 || (writerPID != 0 && sender.Pid != writerPID) {
				invalid = true
			}
			if !invalid {
				writerPID = sender.Pid
			}
		}
		if invalid {
			return 0, fmt.Errorf("observer actual writer credentials missing, changed or forbidden ancillary data")
		}
		return n, readErr
	}, nil
}

func trustedPath(path string, socket bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("observer deployment path invalid")
	}
	for p := path; ; p = filepath.Dir(p) {
		st, err := os.Lstat(p)
		if err != nil {
			return fmt.Errorf("observer deployment path unavailable")
		}
		var stat unix.Stat_t
		if unix.Lstat(p, &stat) != nil || stat.Uid != 0 || st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("observer deployment path not root-owned")
		}
		if p == path && socket {
			if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm()&0o007 != 0 || st.Mode().Perm()&0o111 != 0 {
				return fmt.Errorf("observer socket permissions invalid")
			}
		} else if st.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("observer deployment path writable by non-owner")
		}
		if p == path && !socket && !st.Mode().IsRegular() {
			return fmt.Errorf("observer policy not a regular file")
		}
		if p == "/" {
			break
		}
	}
	return nil
}

func verifyCaller(b CallerBinding) error { return verifyProcess(b, "/proc/self") }

func verifyProcess(b CallerBinding, proc string) error {
	data, err := os.ReadFile(proc + "/stat")
	if err != nil {
		return fmt.Errorf("observer caller identity unavailable")
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return fmt.Errorf("observer caller identity invalid")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 || fields[19] != b.StartTicks {
		return fmt.Errorf("observer caller start identity mismatch")
	}
	f, err := os.Open(proc + "/exe")
	if err != nil {
		return fmt.Errorf("observer caller executable unavailable")
	}
	defer f.Close() //nolint:errcheck
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, 512<<20))
	if err != nil || n >= 512<<20 || hex.EncodeToString(h.Sum(nil)) != b.ControllerBinarySHA256 {
		return fmt.Errorf("observer caller executable pin mismatch")
	}
	return nil
}

// VerifyControllerPeer authenticates the persistent daemon against fixed root
// policy, using kernel credentials and its actual start/executable/boot identity.
// A claimed JSON PID/hash alone cannot establish controller provenance.
func VerifyControllerPeer(conn net.Conn, b CallerBinding) error {
	c, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("controller peer transport invalid")
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return err
	}
	var cred *unix.Ucred
	var peerErr error
	err = raw.Control(func(fd uintptr) { cred, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED) })
	if err != nil || peerErr != nil || cred == nil || int(cred.Pid) != b.PID || cred.Uid != b.UID {
		return fmt.Errorf("controller kernel peer binding mismatch")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(boot)) != b.BootID {
		return fmt.Errorf("controller peer boot mismatch")
	}
	proc := "/proc/" + strconv.Itoa(b.PID)
	if err = verifyProcess(b, proc); err != nil {
		return err
	}
	data, err := os.ReadFile(proc + "/stat")
	if err != nil {
		return fmt.Errorf("controller peer identity unavailable")
	}
	end := strings.LastIndexByte(string(data), ')')
	if end < 0 {
		return fmt.Errorf("controller peer stat invalid")
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 || fields[19] != b.StartTicks {
		return fmt.Errorf("controller peer changed during proof")
	}
	return nil
}
