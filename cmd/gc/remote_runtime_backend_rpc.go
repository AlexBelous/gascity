package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gastownhall/gascity/internal/runtime"
	sshruntime "github.com/gastownhall/gascity/internal/runtime/ssh"
)

type remoteWireRequest struct {
	City          string                 `json:"city"`
	SessionID     string                 `json:"session_id"`
	Template      string                 `json:"template"`
	RuntimeHandle string                 `json:"runtime_handle"`
	Generation    string                 `json:"generation"`
	InstanceToken string                 `json:"instance_token"`
	Attempt       string                 `json:"attempt"`
	Binding       runtime.RuntimeBinding `json:"binding"`
}
type remoteWireIdentity struct {
	ScopeIdentity  remoteScopeIdentity `json:"-"`
	BootID         string              `json:"boot_id"`
	Unit           string              `json:"unit"`
	InvocationID   string              `json:"invocation_id"`
	CgroupPath     string              `json:"cgroup_path"`
	CgroupDevice   uint64              `json:"cgroup_device"`
	CgroupInode    uint64              `json:"cgroup_inode"`
	SourceSHA      string              `json:"source_sha"`
	ManifestSHA256 string              `json:"manifest_sha256"`
	ModelUID       uint32              `json:"model_uid"`
	TransportUID   uint32              `json:"transport_uid"`
	ScopeEmpty     bool                `json:"-"`
	NeverEntered   bool                `json:"-"`
}
type remoteWireReceipt struct {
	Request            remoteWireRequest  `json:"request"`
	AuthorityReceiptID string             `json:"authority_receipt_id"`
	JournalRevision    uint64             `json:"journal_revision"`
	Phase              string             `json:"phase"`
	Identity           remoteWireIdentity `json:"identity"`
}
type remoteRPCRequest struct {
	SchemaVersion int                `json:"schema_version"`
	Operation     string             `json:"op"`
	Request       remoteWireRequest  `json:"request"`
	Prompt        *string            `json:"prompt,omitempty"`
	Environment   *map[string]string `json:"environment,omitempty"`
}
type remoteScopeIdentity struct {
	CgroupPath string `json:"cgroup_path"`
	BootID     string `json:"boot_id"`
	Device     uint64 `json:"device"`
	Inode      uint64 `json:"inode"`
}
type remoteRPCResponse struct {
	NeverEntered  *bool                `json:"never_entered"`
	SchemaVersion int                  `json:"schema_version"`
	Receipt       remoteWireReceipt    `json:"receipt"`
	ScopeEmpty    *bool                `json:"scope_empty"`
	ScopeIdentity *remoteScopeIdentity `json:"scope_identity"`
}
type (
	remoteRPCExecutor     func(context.Context, []string, []byte) ([]byte, error)
	remoteAuthorityClient struct {
		profile remoteOperatorProfile
		execute remoteRPCExecutor
	}
)

func remoteControlArgs(p remoteOperatorProfile) ([]string, error) {
	endpoint, err := sshruntime.ParseEndpoint(p.Binding.Endpoint)
	if err != nil {
		return nil, err
	}
	// Both the user and SSH options come from the immutable operator policy.
	endpoint.User = p.ControlUser
	target := endpoint.User + "@" + endpoint.Host
	args := []string{"-F", "/dev/null", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "IdentitiesOnly=yes", "-o", "IdentityAgent=none", "-o", "GlobalKnownHostsFile=/dev/null", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-o", "ClearAllForwardings=yes", "-o", "PermitLocalCommand=no", "-o", "ConnectionAttempts=1", "-o", "ConnectTimeout=10", "-o", "UserKnownHostsFile=" + p.KnownHostsPath, "-i", p.ControlKeyPath}
	if endpoint.Port != 0 {
		args = append(args, "-p", strconv.Itoa(endpoint.Port))
	}
	return append(args, "--", target, "rpc"), nil
}

type remoteBoundedOutput struct {
	bytes.Buffer
	limit int
}

func (w *remoteBoundedOutput) Write(data []byte) (int, error) {
	if len(data) > w.limit-w.Len() {
		return 0, errors.New("remote authority response too large")
	}
	return w.Buffer.Write(data)
}

func executeRemoteAuthority(ctx context.Context, args []string, input []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/ssh", args...)
	// No inherited SSH_AUTH_SOCK, config, proxy or caller-provided shell.
	command.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	command.Stdin = bytes.NewReader(input)
	output := &remoteBoundedOutput{limit: remoteOperatorJSONLimit}
	command.Stdout = output
	command.Stderr = io.Discard
	command.WaitDelay = 2 * time.Second
	if err := command.Run(); err != nil {
		return nil, errors.New("remote authority response unconfirmed")
	}
	return output.Bytes(), nil
}

func (c remoteAuthorityClient) call(ctx context.Context, operation string, request remoteWireRequest, prompt *string, environment map[string]string) (remoteWireReceipt, error) {
	if operation != "prepare" && operation != "stage_prompt" && operation != "observe" && operation != "cancel" {
		return remoteWireReceipt{}, errors.New("remote authority operation invalid")
	}
	if request.Binding != c.profile.Binding || !remoteHexPin(request.Attempt, 32) {
		return remoteWireReceipt{}, errors.New("remote authority request identity invalid")
	}
	if operation != "stage_prompt" && (prompt != nil || environment != nil) {
		return remoteWireReceipt{}, errors.New("unexpected remote staging payload")
	}
	if operation == "stage_prompt" && prompt == nil {
		return remoteWireReceipt{}, errors.New("remote prompt unavailable")
	}
	args, err := remoteControlArgs(c.profile)
	if err != nil {
		return remoteWireReceipt{}, err
	}
	var stagingEnvironment *map[string]string
	if operation == "stage_prompt" {
		if environment == nil {
			environment = map[string]string{}
		}
		stagingEnvironment = &environment
	}
	input, err := json.Marshal(remoteRPCRequest{1, operation, request, prompt, stagingEnvironment})
	if err != nil || len(input) > remoteOperatorJSONLimit {
		return remoteWireReceipt{}, errors.New("remote authority request too large")
	}
	execute := c.execute
	if execute == nil {
		execute = executeRemoteAuthority
	}
	output, err := execute(ctx, args, input)
	if err != nil {
		return remoteWireReceipt{}, errors.New("remote authority response unconfirmed")
	}
	if len(output) > remoteOperatorJSONLimit {
		return remoteWireReceipt{}, errors.New("remote authority response too large")
	}
	var response remoteRPCResponse
	if err = strictRemoteJSON(output, &response); err != nil || response.SchemaVersion != 1 || response.ScopeEmpty == nil || response.ScopeIdentity == nil || response.NeverEntered == nil || response.Receipt.Request != request || response.Receipt.AuthorityReceiptID == "" || response.Receipt.JournalRevision == 0 {
		return remoteWireReceipt{}, errors.New("remote authority response identity invalid")
	}
	if *response.ScopeEmpty && (response.ScopeIdentity.BootID != response.Receipt.Identity.BootID || response.ScopeIdentity.Device == 0 || response.ScopeIdentity.Inode == 0 || response.ScopeIdentity.CgroupPath != c.profile.CgroupParent) {
		return remoteWireReceipt{}, errors.New("remote empty scope proof invalid")
	}
	if *response.NeverEntered && (!*response.ScopeEmpty || response.Receipt.Phase != "exited" || response.Receipt.Identity.Unit != "" || response.Receipt.Identity.InvocationID != "" || response.Receipt.Identity.CgroupPath != "" || response.Receipt.Identity.CgroupDevice != 0 || response.Receipt.Identity.CgroupInode != 0) {
		return remoteWireReceipt{}, errors.New("remote never-entered proof invalid")
	}
	response.Receipt.Identity.ScopeEmpty = *response.ScopeEmpty
	response.Receipt.Identity.NeverEntered = *response.NeverEntered
	response.Receipt.Identity.ScopeIdentity = *response.ScopeIdentity
	return response.Receipt, nil
}

// checkRemoteCredentialFile refuses symlink/hardlink/group-readable keys and
// keys owned by an unrelated account. Credentials remain outside the profile.
func checkRemoteCredentialFile(name string, private bool) error {
	if !remoteCleanAbs(name) {
		return errors.New("remote credential path invalid")
	}
	rootInfo, rootErr := os.Lstat("/")
	if rootErr != nil {
		return rootErr
	}
	rootUID, _, rootKnown := remoteFileOwnership(rootInfo)
	if !rootKnown || rootUID != 0 || !rootInfo.IsDir() || rootInfo.Mode().Perm()&0o022 != 0 {
		return errors.New("remote credential root ancestor invalid")
	}
	current := "/"
	parts := strings.Split(strings.TrimPrefix(name, "/"), "/")
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		parent, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 || parent.Mode().Perm()&0o022 != 0 || !remoteCredentialOwnerAllowed(parent) {
			return errors.New("remote credential ancestor invalid")
		}
	}
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("remote credential must be a regular file")
	}
	if private && info.Mode().Perm() != 0o600 || info.Mode().Perm()&0o022 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return errors.New("remote credential permissions invalid")
	}
	if err := checkRemoteCredentialOwner(info); err != nil {
		return err
	}
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	if !os.SameFile(info, opened) {
		_ = file.Close()
		return errors.New("remote credential changed")
	}
	return file.Close()
}
