//go:build linux && integration

package procobserver

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestVerifyControllerPeerRequiresKernelIdentity(t *testing.T) {
	raw, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		t.Fatal(err)
	}
	end := strings.LastIndexByte(string(raw), ')')
	fields := strings.Fields(string(raw[end+1:]))
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(binary)
	b := CallerBinding{PID: os.Getpid(), UID: uint32(os.Getuid()), StartTicks: fields[19], BootID: strings.TrimSpace(string(boot)), ControllerBinarySHA256: hex.EncodeToString(hash[:])}
	lis, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(t.TempDir(), "peer.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close() //nolint:errcheck // isolated fixture cleanup
	c, err := net.Dial("unix", lis.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close() //nolint:errcheck // isolated fixture cleanup
	server, err := lis.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close() //nolint:errcheck // isolated fixture cleanup
	if err = VerifyControllerPeer(c, b); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"PID", "UID", "start", "boot", "hash"} {
		t.Run(field, func(t *testing.T) {
			wrong := b
			switch field {
			case "PID":
				wrong.PID++
			case "UID":
				wrong.UID++
			case "start":
				wrong.StartTicks = strconv.Itoa(1)
			case "boot":
				wrong.BootID = "wrong"
			case "hash":
				wrong.ControllerBinarySHA256 = strings.Repeat("f", 64)
			}
			if VerifyControllerPeer(c, wrong) == nil {
				t.Fatal("claimed identity replaced kernel proof")
			}
		})
	}
}

func TestHelperResponseAuthenticatesActualWriterCredentials(t *testing.T) {
	for _, mode := range []string{"exact", "wrong helper UID", "rights"} {
		t.Run(mode, func(t *testing.T) {
			dir, err := os.MkdirTemp("/tmp", "gcwriter-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir) //nolint:errcheck
			address := &net.UnixAddr{Name: filepath.Join(dir, "socket"), Net: "unix"}
			lis, err := net.ListenUnix("unix", address)
			if err != nil {
				t.Fatal(err)
			}
			defer lis.Close() //nolint:errcheck
			client, err := net.DialUnix("unix", nil, address)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close() //nolint:errcheck
			peer, err := lis.AcceptUnix()
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Close() //nolint:errcheck
			uid := uint32(os.Getuid())
			if mode == "wrong helper UID" {
				uid++
			}
			read, err := newWriterAuthenticatedReader(client, uid)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				var e error
				if mode == "rights" {
					f, x := os.Open("/dev/null")
					if x != nil {
						done <- x
						return
					}
					defer f.Close() //nolint:errcheck
					_, _, e = peer.WriteMsgUnix([]byte{0, 0, 0, 1, 'x'}, unix.UnixRights(int(f.Fd())), nil)
				} else {
					e = writeFrame(peer, []byte("x"))
				}
				done <- e
			}()
			data, err := readFrameWithReader(read, 8)
			if (err == nil) != (mode == "exact") || (mode == "exact" && string(data) != "x") {
				t.Fatalf("writer proof mode=%s error=%v", mode, err)
			}
			if e := <-done; e != nil {
				t.Fatal(e)
			}
			if verifyActivationPeer(client) == nil {
				t.Fatal("ordinary fixture listener forged root PID1 activation")
			}
		})
	}
}
