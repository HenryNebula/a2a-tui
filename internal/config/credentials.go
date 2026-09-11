package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Credential scheme names.
const (
	SchemeBearer = "bearer"
	SchemeAPIKey = "apikey"
)

// EnvPrefix marks a stored credential value as an environment variable
// reference ("env:VARNAME"); the secret itself never touches disk.
const EnvPrefix = "env:"

// Credential is one stored agent credential, keyed in credentials.json by
// agent name or URL.
type Credential struct {
	// Scheme is "bearer" (Authorization: Bearer …) or "apikey".
	Scheme string `json:"scheme"`
	// Value is the literal secret or an "env:VARNAME" reference.
	Value string `json:"value"`
	// Header is the header name for apikey credentials (optional).
	Header string `json:"header,omitempty"`
}

// Credentials is the parsed contents of credentials.json.
type Credentials struct {
	Tokens map[string]Credential `json:"tokens"`
}

// LoadCredentials reads credentials.json. A missing file yields empty
// credentials, not an error.
func (s *Store) LoadCredentials() (*Credentials, error) {
	if s == nil {
		return nil, ErrUnavailable
	}
	data, err := os.ReadFile(s.CredentialsPath())
	if errors.Is(err, os.ErrNotExist) {
		return &Credentials{Tokens: map[string]Credential{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.CredentialsPath(), err)
	}
	var c Credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.CredentialsPath(), err)
	}
	if c.Tokens == nil {
		c.Tokens = map[string]Credential{}
	}
	return &c, nil
}

// SaveCredentials writes credentials.json atomically with 0600
// permissions, creating the directory if needed.
func (s *Store) SaveCredentials(c *Credentials) error {
	if s == nil {
		return ErrUnavailable
	}
	if c == nil {
		return errors.New("save credentials: nil credentials")
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	return s.writeFile("credentials.json", data, 0o600)
}

// Set validates and stores cred under key (an agent name or URL).
func (c *Credentials) Set(key string, cred Credential) error {
	switch cred.Scheme {
	case SchemeBearer, SchemeAPIKey:
	case "":
		return fmt.Errorf("credential %q: scheme is required (%q or %q)", key, SchemeBearer, SchemeAPIKey)
	default:
		return fmt.Errorf("credential %q: unknown scheme %q", key, cred.Scheme)
	}
	if strings.TrimSpace(cred.Value) == "" {
		return fmt.Errorf("credential %q: value is required", key)
	}
	if c.Tokens == nil {
		c.Tokens = map[string]Credential{}
	}
	c.Tokens[key] = cred
	return nil
}

// Lookup returns the first credential found under any of the given keys
// (an agent's name, its URL, or both).
func (c *Credentials) Lookup(keys ...string) (Credential, bool) {
	if c == nil {
		return Credential{}, false
	}
	for _, k := range keys {
		if cred, ok := c.Tokens[k]; ok {
			return cred, true
		}
	}
	return Credential{}, false
}

// Resolve returns the usable secret. An "env:VARNAME" reference is looked
// up in the environment at call time; resolved values are never written
// back to disk.
func (cred Credential) Resolve() (string, error) {
	if !strings.HasPrefix(cred.Value, EnvPrefix) {
		return cred.Value, nil
	}
	name := strings.TrimPrefix(cred.Value, EnvPrefix)
	if name == "" {
		return "", errors.New("credential: empty env: reference")
	}
	v, ok := os.LookupEnv(name)
	if !ok || v == "" {
		return "", fmt.Errorf("credential: environment variable %s is not set", name)
	}
	return v, nil
}
