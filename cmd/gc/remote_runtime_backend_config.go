package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/runtime"
	sshruntime "github.com/gastownhall/gascity/internal/runtime/ssh"
)

const (
	remoteOperatorProfilePath = "/etc/gascity/remote-runtime-profiles.json"
	remoteOperatorJSONLimit   = 1 << 20
)

type remoteOperatorProfiles struct {
	Version  int                     `json:"version"`
	Profiles []remoteOperatorProfile `json:"profiles"`
}
type remoteOperatorProfile struct {
	Template           string                 `json:"template"`
	AgentCommandSHA256 string                 `json:"agent_command_sha256"`
	City               string                 `json:"city"`
	Binding            runtime.RuntimeBinding `json:"binding"`
	SourceSHA          string                 `json:"source_sha"`
	ManifestSHA256     string                 `json:"manifest_sha256"`
	ModelUID           uint32                 `json:"model_uid"`
	TransportUID       uint32                 `json:"transport_uid"`
	UnitPrefix         string                 `json:"unit_prefix"`
	CgroupParent       string                 `json:"cgroup_parent"`
	ControlUser        string                 `json:"control_user"`
	ControlKeyPath     string                 `json:"control_key_path"`
	TransportKeyPath   string                 `json:"transport_key_path"`
	KnownHostsPath     string                 `json:"known_hosts_path"`
	EntryPath          string                 `json:"entry_path"`
	TransportWorkDir   string                 `json:"transport_work_dir"`
}

func remoteHexPin(v string, n int) bool {
	if len(v) != n {
		return false
	}
	for _, c := range v {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func remoteSafeWord(v string) bool {
	if v == "" || v[0] == '-' {
		return false
	}
	for _, c := range v {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '-' && c != '.' {
			return false
		}
	}
	return true
}

func remoteCleanAbs(v string) bool {
	return filepath.IsAbs(v) && filepath.Clean(v) == v && v != "/" && !strings.ContainsAny(v, "\x00\r\n")
}

func validateRemoteOperatorProfile(p remoteOperatorProfile) error {
	if p.Template == "" || strings.ContainsAny(p.Template, " \t\r\n\x00") || !remoteHexPin(p.AgentCommandSHA256, 64) || !remoteCleanAbs(p.City) || !strings.HasPrefix(p.Binding.Selector, "ssh:") || p.Binding.Endpoint != strings.TrimPrefix(p.Binding.Selector, "ssh:") ||
		p.Binding.Endpoint == "" || p.Binding.ResourceDomain == "" || !remoteHexPin(p.Binding.PolicyDigest, 64) ||
		!remoteHexPin(p.SourceSHA, 40) || !remoteHexPin(p.ManifestSHA256, 64) || p.ModelUID == 0 || p.TransportUID == 0 || p.ModelUID == p.TransportUID ||
		!remoteSafeWord(p.UnitPrefix) || strings.HasSuffix(p.UnitPrefix, "-") || !remoteCleanAbs(p.CgroupParent) || !remoteSafeWord(p.ControlUser) ||
		!remoteCleanAbs(p.ControlKeyPath) || !remoteCleanAbs(p.TransportKeyPath) || !remoteCleanAbs(p.KnownHostsPath) || !remoteCleanAbs(p.EntryPath) || !remoteCleanAbs(p.TransportWorkDir) {
		return errors.New("remote operator profile incomplete")
	}
	if len(p.UnitPrefix) > 64 {
		return errors.New("remote unit prefix too long")
	}
	for _, c := range p.UnitPrefix {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return errors.New("remote unit prefix invalid")
		}
	}
	endpoint, err := sshruntime.ParseEndpoint(p.Binding.Endpoint)
	if err != nil || !remoteSafeWord(endpoint.User) || endpoint.User == p.ControlUser || strings.ContainsAny(endpoint.Host, " \t\r\n/\\\x00") || strings.HasPrefix(endpoint.Host, "-") {
		return errors.New("remote endpoint invalid")
	}
	for _, part := range strings.Split(strings.TrimPrefix(p.CgroupParent, "/"), "/") {
		if !strings.HasSuffix(part, ".slice") || !remoteSafeWord(part) {
			return errors.New("remote cgroup parent invalid")
		}
	}
	return nil
}

// rejectRemoteDuplicateKeys validates the complete JSON token stream before
// typed decoding; encoding/json otherwise silently accepts duplicate keys.
func rejectRemoteDuplicateKeys(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	switch token {
	case json.Delim('{'):
		seen := map[string]bool{}
		for dec.More() {
			key, e := dec.Token()
			if e != nil {
				return e
			}
			s, ok := key.(string)
			if !ok || seen[s] {
				return errors.New("duplicate or invalid JSON key")
			}
			seen[s] = true
			if e = rejectRemoteDuplicateKeys(dec); e != nil {
				return e
			}
		}
		end, e := dec.Token()
		if e != nil || end != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case json.Delim('['):
		for dec.More() {
			if e := rejectRemoteDuplicateKeys(dec); e != nil {
				return e
			}
		}
		end, e := dec.Token()
		if e != nil || end != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	case json.Delim('}'), json.Delim(']'):
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func strictRemoteJSON(data []byte, target any) error {
	scan := json.NewDecoder(bytes.NewReader(data))
	if err := rejectRemoteDuplicateKeys(scan); err != nil {
		return err
	}
	if _, err := scan.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(target)
}

func decodeRemoteOperatorProfiles(reader io.Reader) (remoteOperatorProfiles, error) {
	data, err := io.ReadAll(io.LimitReader(reader, remoteOperatorJSONLimit+1))
	if err != nil {
		return remoteOperatorProfiles{}, err
	}
	if len(data) > remoteOperatorJSONLimit {
		return remoteOperatorProfiles{}, errors.New("remote profile too large")
	}
	var profiles remoteOperatorProfiles
	if err = strictRemoteJSON(data, &profiles); err != nil {
		return profiles, err
	}
	if profiles.Version != 1 {
		return profiles, errors.New("unsupported remote profile version")
	}
	seen := map[string]bool{}
	for _, p := range profiles.Profiles {
		if err = validateRemoteOperatorProfile(p); err != nil {
			return profiles, err
		}
		if seen[p.Binding.Selector] {
			return profiles, errors.New("duplicate remote selector")
		}
		seen[p.Binding.Selector] = true
	}
	return profiles, nil
}

// openRootOwnedRemoteFile checks every ancestor before opening. Root ownership
// and non-writable ancestors prevent replacement between lstat and open.
func openRootOwnedRemoteFile(name string) (*os.File, error) {
	if !remoteCleanAbs(name) {
		return nil, errors.New("remote policy path invalid")
	}
	rootInfo, rootErr := os.Lstat("/")
	if rootErr != nil {
		return nil, rootErr
	}
	rootUID, _, rootKnown := remoteFileOwnership(rootInfo)
	if !rootKnown || rootUID != 0 || !rootInfo.IsDir() || rootInfo.Mode().Perm()&0o022 != 0 {
		return nil, errors.New("remote policy root ancestor invalid")
	}
	current := "/"
	parts := strings.Split(strings.TrimPrefix(name, "/"), "/")
	for i, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return nil, err
		}
		uid, links, ok := remoteFileOwnership(info)
		if !ok || uid != 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
			return nil, errors.New("remote policy ownership invalid")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return nil, errors.New("remote policy parent invalid")
		}
		if i == len(parts)-1 && (!info.Mode().IsRegular() || links != 1) {
			return nil, errors.New("remote policy file invalid")
		}
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	before, err := os.Lstat(name)
	if err != nil || !os.SameFile(before, info) {
		return nil, errors.Join(errors.New("remote policy changed"), file.Close())
	}
	return file, nil
}

func loadRemoteOperatorProfiles() (remoteOperatorProfiles, error) {
	file, err := openRootOwnedRemoteFile(remoteOperatorProfilePath)
	if err != nil {
		return remoteOperatorProfiles{}, fmt.Errorf("remote operator profile unavailable: %w", err)
	}
	profiles, decodeErr := decodeRemoteOperatorProfiles(file)
	if err := errors.Join(decodeErr, file.Close()); err != nil {
		return remoteOperatorProfiles{}, err
	}
	return profiles, nil
}

func checkRemoteCredentialOwner(info os.FileInfo) error {
	uid, links, ok := remoteFileOwnership(info)
	if !ok || links != 1 || (uid != 0 && uid != uint32(os.Geteuid())) {
		return errors.New("remote credential ownership invalid")
	}
	return nil
}

func remoteCredentialOwnerAllowed(info os.FileInfo) bool {
	uid, _, ok := remoteFileOwnership(info)
	return ok && (uid == 0 || uid == uint32(os.Geteuid()))
}
