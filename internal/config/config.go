// Package config loads and saves a2a-tui user configuration: the saved
// agent list (config.json) and per-agent credentials (credentials.json).
//
// Both files live in os.UserConfigDir()/a2a-tui/. Writes are atomic
// (temp file + rename) and the directory is created on first save. A
// missing file loads as empty, never as an error. Credential values may
// reference environment variables ("env:VARNAME") instead of storing
// secrets; resolved values are never persisted.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// configVersion is the on-disk schema version of config.json.
const configVersion = 1

// ErrUnavailable is returned by Store methods on a nil Store (no usable
// user config directory).
var ErrUnavailable = errors.New("config store unavailable")

// Agent is one saved agent endpoint.
type Agent struct {
	// Name is the local alias used to reference the agent from the UI.
	Name string `json:"name"`
	// URL is the agent base URL (or card URL) to connect to.
	URL string `json:"url"`
	// Protocol pins the wire protocol: "" (auto-detect), "1.0" or "0.3".
	Protocol string `json:"protocol,omitempty"`
	// AuthRef names the credentials.json entry used to authenticate.
	AuthRef string `json:"auth_ref,omitempty"`
}

// File is the parsed contents of config.json.
type File struct {
	Version      int     `json:"version"`
	DefaultAgent string  `json:"default_agent,omitempty"`
	Agents       []Agent `json:"agents,omitempty"`
}

// Store reads and writes configuration files under a fixed directory.
// The zero value is not useful; use DefaultStore or Open.
type Store struct {
	// Dir is the directory holding config.json and credentials.json.
	Dir string
}

// DefaultStore returns a Store rooted at os.UserConfigDir()/a2a-tui.
func DefaultStore() (*Store, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("locate user config dir: %w", err)
	}
	return &Store{Dir: filepath.Join(base, "a2a-tui")}, nil
}

// Open returns a Store rooted at dir.
func Open(dir string) *Store { return &Store{Dir: dir} }

// ConfigPath is the absolute path of config.json.
func (s *Store) ConfigPath() string { return filepath.Join(s.Dir, "config.json") }

// CredentialsPath is the absolute path of credentials.json.
func (s *Store) CredentialsPath() string { return filepath.Join(s.Dir, "credentials.json") }

// Load reads config.json. A missing file yields an empty File, not an
// error. s may be nil, in which case ErrUnavailable is returned.
func (s *Store) Load() (*File, error) {
	if s == nil {
		return nil, ErrUnavailable
	}
	data, err := os.ReadFile(s.ConfigPath())
	if errors.Is(err, os.ErrNotExist) {
		return &File{Version: configVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.ConfigPath(), err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.ConfigPath(), err)
	}
	switch {
	case f.Version == 0:
		f.Version = configVersion // tolerate files written without a version
	case f.Version > configVersion:
		return nil, fmt.Errorf("%s: schema version %d is newer than supported %d", s.ConfigPath(), f.Version, configVersion)
	}
	return &f, nil
}

// Save writes config.json atomically, creating the directory if needed.
func (s *Store) Save(f *File) error {
	if s == nil {
		return ErrUnavailable
	}
	if f == nil {
		return errors.New("save: nil config file")
	}
	f.Version = configVersion
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return s.writeFile("config.json", data, 0o644)
}

// Agent returns the saved agent with the given name (exact match).
func (f *File) Agent(name string) (Agent, bool) {
	if f == nil {
		return Agent{}, false
	}
	for _, a := range f.Agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

// UpsertAgent adds ag, replacing any existing entry with the same name.
func (f *File) UpsertAgent(ag Agent) {
	for i, cur := range f.Agents {
		if cur.Name == ag.Name {
			f.Agents[i] = ag
			return
		}
	}
	f.Agents = append(f.Agents, ag)
}

// RemoveAgent deletes the named agent and reports whether it existed.
// A DefaultAgent pointing at the removed name is cleared.
func (f *File) RemoveAgent(name string) bool {
	if f == nil {
		return false
	}
	for i, cur := range f.Agents {
		if cur.Name == name {
			f.Agents = append(f.Agents[:i], f.Agents[i+1:]...)
			if f.DefaultAgent == name {
				f.DefaultAgent = ""
			}
			return true
		}
	}
	return false
}

// writeFile writes data to name in the store directory atomically: it is
// staged in a temp file on the same filesystem, synced, permissioned,
// then renamed over the target.
func (s *Store) writeFile(name string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("create config dir %s: %w", s.Dir, err)
	}
	tmp, err := os.CreateTemp(s.Dir, "."+name+".tmp-*")
	if err != nil {
		return fmt.Errorf("stage %s: %w", name, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed
	fail := func(err error) error {
		_ = tmp.Close()
		return fmt.Errorf("stage %s: %w", name, err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("stage %s: %w", name, err)
	}
	if err := os.Rename(tmpName, filepath.Join(s.Dir, name)); err != nil {
		return fmt.Errorf("commit %s: %w", name, err)
	}
	return nil
}
