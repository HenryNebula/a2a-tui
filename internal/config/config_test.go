package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingFiles(t *testing.T) {
	s := Open(t.TempDir())

	f, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if f.Version != configVersion {
		t.Errorf("Version = %d, want %d", f.Version, configVersion)
	}
	if len(f.Agents) != 0 || f.DefaultAgent != "" {
		t.Errorf("want empty file, got %+v", f)
	}

	c, err := s.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if len(c.Tokens) != 0 {
		t.Errorf("want empty tokens, got %+v", c.Tokens)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := Open(t.TempDir())

	in := &File{
		DefaultAgent: "primary",
		Agents: []Agent{
			{Name: "primary", URL: "https://agent.example.com", Protocol: "1.0", AuthRef: "primary"},
			{Name: "legacy", URL: "https://old.example.com", Protocol: "0.3"},
		},
	}
	if err := s.Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.DefaultAgent != "primary" || len(out.Agents) != 2 {
		t.Fatalf("round trip mismatch: %+v", out)
	}
	got, ok := out.Agent("legacy")
	if !ok || got.URL != "https://old.example.com" || got.Protocol != "0.3" || got.AuthRef != "" {
		t.Errorf("Agent(legacy) = %+v, ok=%v", got, ok)
	}
	if _, ok := out.Agent("nope"); ok {
		t.Error("Agent(nope) unexpectedly found")
	}
}

func TestSaveCreatesDirAndLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	s := Open(filepath.Join(dir, "a2a-tui")) // does not exist yet

	if err := s.Save(&File{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Errorf("dir contains %d entries, want only config.json", len(entries))
	}
}

func TestCredentialsPermissionsAndRoundTrip(t *testing.T) {
	s := Open(t.TempDir())

	in := &Credentials{Tokens: map[string]Credential{
		"agent.example.com": {Scheme: SchemeBearer, Value: "token-1"},
	}}
	if err := s.SaveCredentials(in); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	info, err := os.Stat(s.CredentialsPath())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials.json perms = %o, want 600", perm)
	}

	out, err := s.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials: %v", err)
	}
	if got, ok := out.Lookup("agent.example.com"); !ok || got != (Credential{Scheme: SchemeBearer, Value: "token-1"}) {
		t.Errorf("Lookup = %+v, ok=%v", got, ok)
	}

	in.Set("byname", Credential{Scheme: SchemeAPIKey, Value: "k", Header: "X-Key"})
	if err := s.SaveCredentials(in); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if out, _ := s.LoadCredentials(); len(out.Tokens) != 2 {
		t.Errorf("tokens = %d, want 2", len(out.Tokens))
	}
}

func TestCredentialResolve(t *testing.T) {
	t.Setenv("A2A_TUI_TEST_TOKEN", "from-env")

	tests := []struct {
		name    string
		cred    Credential
		want    string
		wantErr bool
	}{
		{"literal", Credential{Scheme: SchemeBearer, Value: "abc"}, "abc", false},
		{"env ref", Credential{Scheme: SchemeBearer, Value: "env:A2A_TUI_TEST_TOKEN"}, "from-env", false},
		{"env unset", Credential{Scheme: SchemeBearer, Value: "env:A2A_TUI_TEST_MISSING"}, "", true},
		{"env empty name", Credential{Scheme: SchemeBearer, Value: "env:"}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.cred.Resolve()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Resolve() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if got != tt.want {
				t.Errorf("Resolve() = %q, want %q", got, tt.want)
			}
		})
	}

	// Resolved env values must never be persisted.
	s := Open(t.TempDir())
	creds := &Credentials{}
	creds.Set("x", Credential{Scheme: SchemeBearer, Value: "env:A2A_TUI_TEST_TOKEN"})
	if _, err := creds.Tokens["x"].Resolve(); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	s.SaveCredentials(creds)
	out, _ := s.LoadCredentials()
	if out.Tokens["x"].Value != "env:A2A_TUI_TEST_TOKEN" {
		t.Errorf("stored value = %q, want env: reference", out.Tokens["x"].Value)
	}
}

func TestCredentialSetValidation(t *testing.T) {
	tests := []struct {
		name    string
		cred    Credential
		wantErr string
	}{
		{"bearer ok", Credential{Scheme: SchemeBearer, Value: "t"}, ""},
		{"apikey ok", Credential{Scheme: SchemeAPIKey, Value: "t"}, ""},
		{"no scheme", Credential{Value: "t"}, "scheme is required"},
		{"bad scheme", Credential{Scheme: "digest", Value: "t"}, "unknown scheme"},
		{"no value", Credential{Scheme: SchemeBearer, Value: " "}, "value is required"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Credentials{}
			err := c.Set("k", tt.cred)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Set: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Set error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestAgentUpsertRemoveDefault(t *testing.T) {
	f := &File{}
	f.UpsertAgent(Agent{Name: "a", URL: "https://a"})
	f.UpsertAgent(Agent{Name: "b", URL: "https://b"})
	f.DefaultAgent = "a"

	f.UpsertAgent(Agent{Name: "a", URL: "https://a2"}) // replace, not append
	if len(f.Agents) != 2 {
		t.Fatalf("agents = %d, want 2", len(f.Agents))
	}
	if a, _ := f.Agent("a"); a.URL != "https://a2" {
		t.Errorf("a.URL = %q, want replacement", a.URL)
	}

	if !f.RemoveAgent("a") {
		t.Error("RemoveAgent(a) = false, want true")
	}
	if f.RemoveAgent("a") {
		t.Error("RemoveAgent(a) twice = true, want false")
	}
	if f.DefaultAgent != "" {
		t.Errorf("DefaultAgent = %q, want cleared", f.DefaultAgent)
	}
}

func TestLoadCorruptFileErrors(t *testing.T) {
	s := Open(t.TempDir())
	os.WriteFile(s.ConfigPath(), []byte("{not json"), 0o644)
	if _, err := s.Load(); err == nil {
		t.Error("Load on corrupt file: want error")
	}
	os.WriteFile(s.CredentialsPath(), []byte("{not json"), 0o600)
	if _, err := s.LoadCredentials(); err == nil {
		t.Error("LoadCredentials on corrupt file: want error")
	}
}

func TestLoadNewerVersionErrors(t *testing.T) {
	s := Open(t.TempDir())
	os.WriteFile(s.ConfigPath(), []byte(`{"version":99}`), 0o644)
	_, err := s.Load()
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Errorf("Load version 99: err = %v, want newer-schema error", err)
	}
}

func TestNilStore(t *testing.T) {
	var s *Store
	if _, err := s.Load(); !errors.Is(err, ErrUnavailable) {
		t.Errorf("nil Load: %v, want ErrUnavailable", err)
	}
	if _, err := s.LoadCredentials(); !errors.Is(err, ErrUnavailable) {
		t.Errorf("nil LoadCredentials: %v, want ErrUnavailable", err)
	}
	if err := s.Save(&File{}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("nil Save: %v, want ErrUnavailable", err)
	}
	if err := s.SaveCredentials(&Credentials{}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("nil SaveCredentials: %v, want ErrUnavailable", err)
	}
}
