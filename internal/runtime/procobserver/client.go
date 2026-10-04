// Package procobserver reads bounded, pinned process evidence from a separate
// Linux observer. It has no lifecycle, privilege, process-launch or store API.
package procobserver

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/proctable"
)

// Fixed protocol versions and bounds cannot be increased by a client.
const (
	Schema           = "host-process-evidence/v1"
	RequestSchema    = "observe-host-processes/v1"
	MaxRequestBytes  = 1024
	MaxResponseBytes = 16 << 20
	MaxProcesses     = 65536
	Timeout          = 10 * time.Second
)

// Request is the sole bounded helper operation and its replay nonce.
type Request struct {
	Schema       string `json:"schema"`
	RequestNonce string `json:"request_nonce"`
}

// CallerBinding is the immutable deployment pin of the actual connecting process.
type CallerBinding struct {
	PID                      int    `json:"pid"`
	UID                      uint32 `json:"uid"`
	StartTicks               string `json:"start_ticks"`
	BootID                   string `json:"boot_id"`
	ControllerSourceRevision string `json:"controller_source_revision"`
	ControllerBinarySHA256   string `json:"controller_binary_sha256"`
}

// Policy is the exact root-owned helper, host and caller release agreement.
type Policy struct {
	SocketPath           string        `json:"socket_path"`
	HelperUID            uint32        `json:"helper_uid"`
	HelperSourceRevision string        `json:"helper_source_revision"`
	HelperBinarySHA256   string        `json:"helper_binary_sha256"`
	PolicyDigest         string        `json:"policy_digest"`
	BootID               string        `json:"boot_id"`
	PIDNamespaceIdentity string        `json:"pid_namespace_identity"`
	CallerBinding        CallerBinding `json:"caller_binding"`
}

// Root is one redacted host process incarnation without provider authority.
type Root struct {
	PID                            int    `json:"pid"`
	PPID                           int    `json:"ppid"`
	PGID                           int    `json:"pgid"`
	StartTicks                     string `json:"start_ticks"`
	SessionID                      string `json:"session_id"`
	City                           string `json:"city"`
	Template                       string `json:"template"`
	Epoch                          int    `json:"epoch"`
	InstanceTokenSHA256            string `json:"instance_token_sha256"`
	Name                           string `json:"name"`
	ParentName                     string `json:"parent_name"`
	ParentIsProviderInfrastructure bool   `json:"parent_is_provider_infrastructure"`
}

// EvidenceError is a bounded diagnostic containing no environment or token contents.
type EvidenceError struct {
	Reason    string `json:"reason"`
	PID       int    `json:"pid,omitempty"`
	Operation string `json:"operation"`
	Errno     int    `json:"errno"`
}

// Response is the independent process-only evidence, including coverage failures.
type Response struct {
	Schema                  string          `json:"schema"`
	Scope                   string          `json:"scope"`
	RequestNonce            string          `json:"request_nonce"`
	HelperSourceRevision    string          `json:"helper_source_revision"`
	HelperBinarySHA256      string          `json:"helper_binary_sha256"`
	PolicyDigest            string          `json:"policy_digest"`
	BootID                  string          `json:"boot_id"`
	PIDNamespaceIdentity    string          `json:"pid_namespace_identity"`
	StartedAt               time.Time       `json:"started_at"`
	FinishedAt              time.Time       `json:"finished_at"`
	DurationMS              int64           `json:"duration_ms"`
	Complete                bool            `json:"complete"`
	EnumeratedCountBefore   int             `json:"enumerated_count_before"`
	EnumeratedCountAfter    int             `json:"enumerated_count_after"`
	EnumerationDigestBefore string          `json:"enumeration_digest_before"`
	EnumerationDigestAfter  string          `json:"enumeration_digest_after"`
	Roots                   []Root          `json:"roots"`
	Errors                  []EvidenceError `json:"errors"`
	ErrorsTotal             int             `json:"errors_total"`
	ErrorsTruncated         bool            `json:"errors_truncated"`
	CallerBinding           CallerBinding   `json:"caller_binding"`
}

// ObservedRoots converts only redacted fields. Err from Read must remain attached;
// partial or empty roots never establish complete coverage.
func (r Response) ObservedRoots() []proctable.ObservedRoot {
	out := make([]proctable.ObservedRoot, 0, len(r.Roots))
	for _, v := range r.Roots {
		out = append(out, proctable.ObservedRoot{Runtime: runtime.LiveRuntime{PID: v.PID, PPID: v.PPID, SessionID: v.SessionID, City: v.City, Epoch: v.Epoch, Name: v.Name, ParentIsProviderInfrastructure: v.ParentIsProviderInfrastructure}, Template: v.Template, StartIdentity: v.StartTicks, InstanceTokenSHA256: v.InstanceTokenSHA256})
	}
	return out
}

// LoadPolicy accepts only a root-owned immutable deployment policy, with an
// exact source pin. It does not create or refresh caller bindings.
func LoadPolicy(path, source string) (Policy, error) {
	var p Policy
	if err := trustedPath(path, false); err != nil {
		return p, err
	}
	f, err := os.Open(path)
	if err != nil {
		return p, fmt.Errorf("observer policy unavailable")
	}
	defer f.Close() //nolint:errcheck
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return p, fmt.Errorf("observer policy exceeds bound")
	}
	if strictJSON(data, &p) != nil || validatePolicy(p) != nil || p.HelperSourceRevision != source || p.CallerBinding.ControllerSourceRevision != source {
		return Policy{}, fmt.Errorf("observer policy source or contract invalid")
	}
	return p, nil
}

func validatePolicy(p Policy) error {
	if p.HelperUID == 0 || !filepath.IsAbs(p.SocketPath) || filepath.Clean(p.SocketPath) != p.SocketPath || !isHex(p.HelperSourceRevision, 40) || !isHex(p.HelperBinarySHA256, 64) || !isHex(p.PolicyDigest, 64) || p.BootID == "" || p.PIDNamespaceIdentity == "" || p.CallerBinding.PID <= 1 || !positiveNumber(p.CallerBinding.StartTicks) || p.CallerBinding.BootID != p.BootID || !isHex(p.CallerBinding.ControllerSourceRevision, 40) || !isHex(p.CallerBinding.ControllerBinarySHA256, 64) {
		return fmt.Errorf("observer policy incomplete")
	}
	return nil
}

// Read opens one AF_UNIX connection. It checks fixed deployment ownership,
// exact caller identity and kernel peer UID before sending any request.
// It never falls back to a best-effort scan when the helper fails.
func Read(p Policy) (Response, error) { return ReadContext(context.Background(), p) }

// CheckCaller proves this process matches the immutable caller source/binary
// binding. It never writes or refreshes that deployment policy.
func CheckCaller(p Policy) error {
	if err := validatePolicy(p); err != nil {
		return err
	}
	if p.CallerBinding.PID != os.Getpid() || p.CallerBinding.UID != uint32(os.Getuid()) {
		return fmt.Errorf("observer caller binding does not match this process")
	}
	return verifyCaller(p.CallerBinding)
}

// ReadContext cancels the socket read on request shutdown/deadline. It does not
// retry or downgrade the pinned helper evidence on failure.
func ReadContext(ctx context.Context, p Policy) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, fmt.Errorf("observer request canceled")
	}
	if err := validatePolicy(p); err != nil {
		return Response{}, err
	}
	if p.CallerBinding.PID != os.Getpid() || p.CallerBinding.UID != uint32(os.Getuid()) {
		return Response{}, fmt.Errorf("observer caller binding does not match this process")
	}
	if err := verifyCaller(p.CallerBinding); err != nil {
		return Response{}, err
	}
	if err := trustedPath(p.SocketPath, true); err != nil {
		return Response{}, err
	}
	dialer := net.Dialer{Timeout: Timeout}
	conn, err := dialer.DialContext(ctx, "unix", p.SocketPath)
	if err != nil {
		return Response{}, fmt.Errorf("observer socket unavailable")
	}
	defer conn.Close() //nolint:errcheck
	c, ok := conn.(*net.UnixConn)
	if !ok {
		return Response{}, fmt.Errorf("observer transport unsupported")
	}
	if err = verifyActivationPeer(c); err != nil {
		return Response{}, fmt.Errorf("observer root activation peer mismatch")
	}
	read, err := newWriterAuthenticatedReader(c, p.HelperUID)
	if err != nil {
		return Response{}, err
	}
	return exchangeContextWithReader(ctx, c, p, time.Now, read)
}

func exchangeContext(ctx context.Context, c *net.UnixConn, p Policy, now func() time.Time) (Response, error) {
	return exchangeContextWithReader(ctx, c, p, now, func(data []byte) (int, error) { return readNoAncillary(c, data) })
}

func exchangeContextWithReader(ctx context.Context, c *net.UnixConn, p Policy, now func() time.Time, read func([]byte) (int, error)) (Response, error) {
	stopClose := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stopClose()
	deadline := time.Now().Add(Timeout)
	if bounded, ok := ctx.Deadline(); ok && bounded.Before(deadline) {
		deadline = bounded
	}
	if err := c.SetDeadline(deadline); err != nil {
		return Response{}, fmt.Errorf("observer deadline unavailable")
	}
	return exchangeWithReader(c, p, now, read)
}

func exchange(c *net.UnixConn, p Policy, now func() time.Time) (Response, error) {
	return exchangeWithReader(c, p, now, func(data []byte) (int, error) { return readNoAncillary(c, data) })
}

func exchangeWithReader(c *net.UnixConn, p Policy, now func() time.Time, read func([]byte) (int, error)) (Response, error) {
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return Response{}, fmt.Errorf("observer nonce unavailable")
	}
	request := Request{Schema: RequestSchema, RequestNonce: hex.EncodeToString(nonce)}
	data, err := json.Marshal(request)
	if err != nil {
		return Response{}, fmt.Errorf("observer request unavailable")
	}
	if err = writeFrame(c, data); err != nil {
		return Response{}, fmt.Errorf("observer request write failed")
	}
	if err = c.CloseWrite(); err != nil {
		return Response{}, fmt.Errorf("observer request boundary failed")
	}
	data, err = readFrameWithReader(read, MaxResponseBytes)
	if err != nil {
		return Response{}, err
	}
	var extra [1]byte
	n, err := read(extra[:])
	if n != 0 || !errors.Is(err, io.EOF) {
		return Response{}, fmt.Errorf("observer response has extra data or no close")
	}
	return decodeResponse(data, p, request.RequestNonce, now().UTC())
}

func strictJSON(data []byte, out any) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("wire encoding invalid")
	}
	if err := validateRequiredTypes(data, reflect.TypeOf(out).Elem()); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("extra JSON data")
	}
	return nil
}

func decodeResponse(data []byte, p Policy, nonce string, now time.Time) (Response, error) {
	var r Response
	if len(data) > MaxResponseBytes || strictJSON(data, &r) != nil {
		return Response{}, fmt.Errorf("observer response contract invalid")
	}
	if r.Schema != Schema || r.Scope != "host_procfs" || r.RequestNonce != nonce || r.HelperSourceRevision != p.HelperSourceRevision || r.HelperBinarySHA256 != p.HelperBinarySHA256 || r.PolicyDigest != p.PolicyDigest || r.BootID != p.BootID || r.PIDNamespaceIdentity != p.PIDNamespaceIdentity || r.CallerBinding != p.CallerBinding {
		return Response{}, fmt.Errorf("observer response binding mismatch")
	}
	if r.StartedAt.IsZero() || r.FinishedAt.IsZero() || r.StartedAt.After(r.FinishedAt) || r.FinishedAt.After(now) || now.Sub(r.StartedAt) > 60*time.Second || r.DurationMS < 0 || r.DurationMS > Timeout.Milliseconds() || r.FinishedAt.Sub(r.StartedAt) > Timeout {
		return Response{}, fmt.Errorf("observer response interval invalid or stale")
	}
	if r.Roots == nil || r.Errors == nil || r.EnumeratedCountBefore < 1 || r.EnumeratedCountBefore > MaxProcesses || r.EnumeratedCountAfter < 1 || r.EnumeratedCountAfter > MaxProcesses || !isHex(r.EnumerationDigestBefore, 64) || !isHex(r.EnumerationDigestAfter, 64) || len(r.Roots) > MaxProcesses || len(r.Roots) > r.EnumeratedCountBefore || len(r.Roots) > r.EnumeratedCountAfter || len(r.Errors) > 256 || r.ErrorsTotal < 0 || r.ErrorsTotal < len(r.Errors) {
		return Response{}, fmt.Errorf("observer response coverage fields invalid")
	}
	last := 0
	for _, v := range r.Roots {
		if v.PID <= 1 || v.PID <= last || v.PPID < 0 || v.PGID < 1 || !positiveNumber(v.StartTicks) || !safeIdentity(v.SessionID) || !safeIdentity(v.Template) || !filepath.IsAbs(v.City) || !safeIdentity(v.City) || v.Epoch < 1 || !isHex(v.InstanceTokenSHA256, 64) || len(v.Name) > 256 || len(v.ParentName) > 256 || v.ParentIsProviderInfrastructure != infrastructureName(v.ParentName) {
			return Response{}, fmt.Errorf("observer root identity invalid")
		}
		last = v.PID
	}
	if !r.Complete || r.ErrorsTotal != 0 || len(r.Errors) != 0 || r.ErrorsTruncated || r.EnumeratedCountBefore != r.EnumeratedCountAfter || r.EnumerationDigestBefore != r.EnumerationDigestAfter {
		return r, fmt.Errorf("observer process coverage incomplete")
	}
	return r, nil
}

func infrastructureName(name string) bool {
	name = filepath.Base(strings.TrimSpace(name))
	return name == "tmux" || name == "tmux:" || name == "tmux: server" || name == "tmux: client"
}

func safeIdentity(s string) bool {
	return s != "" && len(s) <= 4096 && !strings.ContainsAny(s, "\x00\r\n")
}

func positiveNumber(s string) bool {
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == s
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func writeFrame(c *net.UnixConn, data []byte) error {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(data)))
	for _, part := range [][]byte{header[:], data} {
		for len(part) > 0 {
			n, err := c.Write(part)
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
			part = part[n:]
		}
	}
	return nil
}

func readNoAncillary(c *net.UnixConn, data []byte) (int, error) {
	oob := make([]byte, 32)
	n, on, flags, _, err := c.ReadMsgUnix(data, oob)
	if on != 0 {
		messages, _ := syscall.ParseSocketControlMessage(oob[:on])
		for _, message := range messages {
			fds, err := syscall.ParseUnixRights(&message)
			if err == nil {
				for _, fd := range fds {
					_ = syscall.Close(fd)
				}
			}
		}
	}
	if on != 0 || flags&(syscall.MSG_CTRUNC|syscall.MSG_TRUNC) != 0 {
		return 0, fmt.Errorf("observer ancillary data rejected")
	}
	return n, err
}

func readFrame(c *net.UnixConn, byteLimit int) ([]byte, error) {
	return readFrameWithReader(func(data []byte) (int, error) { return readNoAncillary(c, data) }, byteLimit)
}

func readFrameWithReader(read func([]byte) (int, error), byteLimit int) ([]byte, error) {
	readFull := func(data []byte) error {
		for len(data) > 0 {
			n, err := read(data)
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrUnexpectedEOF
			}
			data = data[n:]
		}
		return nil
	}
	var header [4]byte
	if readFull(header[:]) != nil {
		return nil, fmt.Errorf("observer frame header incomplete")
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || uint64(size) > uint64(byteLimit) {
		return nil, fmt.Errorf("observer frame exceeds bound")
	}
	data := make([]byte, int(size))
	if readFull(data) != nil {
		return nil, fmt.Errorf("observer frame incomplete or ancillary data")
	}
	return data, nil
}

// JSON null is accepted for scalar Go fields by encoding/json. Require the
// actual typed wire shape and every non-optional key before that conversion.
func validateRequiredTypes(data []byte, typ reflect.Type) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var walk func(reflect.Type, int) error
	walk = func(t reflect.Type, depth int) error {
		if depth > 16 {
			return fmt.Errorf("wire nesting exceeds bound")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		if token == nil {
			return fmt.Errorf("null wire value")
		}
		if t == reflect.TypeOf(time.Time{}) {
			if _, ok := token.(string); !ok {
				return fmt.Errorf("timestamp type invalid")
			}
			return nil
		}
		switch t.Kind() {
		case reflect.Struct:
			if token != json.Delim('{') {
				return fmt.Errorf("wire object type invalid")
			}
			fields := map[string]reflect.StructField{}
			required := map[string]bool{}
			var addFields func(reflect.Type) error
			addFields = func(st reflect.Type) error {
				for i := 0; i < st.NumField(); i++ {
					f := st.Field(i)
					if f.Anonymous && f.Type.Kind() == reflect.Struct && f.Tag.Get("json") == "" {
						if e := addFields(f.Type); e != nil {
							return e
						}
						continue
					}
					tag := strings.Split(f.Tag.Get("json"), ",")
					if tag[0] == "" || tag[0] == "-" {
						return fmt.Errorf("untyped wire field")
					}
					fields[tag[0]] = f
					if len(tag) == 1 || tag[1] != "omitempty" {
						required[tag[0]] = true
					}
				}
				return nil
			}
			if e := addFields(t); e != nil {
				return e
			}
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				name, ok := key.(string)
				field, exists := fields[name]
				if !ok || !exists || seen[name] {
					return fmt.Errorf("unknown or duplicate wire key")
				}
				seen[name] = true
				if e = walk(field.Type, depth+1); e != nil {
					return e
				}
			}
			if _, err = d.Token(); err != nil {
				return err
			}
			for key := range required {
				if !seen[key] {
					return fmt.Errorf("required wire key missing")
				}
			}
		case reflect.Slice:
			if token != json.Delim('[') {
				return fmt.Errorf("wire array type invalid")
			}
			for d.More() {
				if err = walk(t.Elem(), depth+1); err != nil {
					return err
				}
			}
			if _, err = d.Token(); err != nil {
				return err
			}
		case reflect.String:
			if _, ok := token.(string); !ok {
				return fmt.Errorf("wire string type invalid")
			}
		case reflect.Bool:
			if _, ok := token.(bool); !ok {
				return fmt.Errorf("wire boolean type invalid")
			}
		case reflect.Int, reflect.Int64, reflect.Uint32:
			if _, ok := token.(json.Number); !ok {
				return fmt.Errorf("wire numeric type invalid")
			}
		default:
			return fmt.Errorf("wire type unsupported")
		}
		return nil
	}
	if err := walk(typ, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("extra wire data")
	}
	return nil
}

// DecodeStrict checks required keys/types, duplicate keys and exact JSON
// framing before decoding a typed local observation envelope.
func DecodeStrict(data []byte, out any) error { return strictJSON(data, out) }
