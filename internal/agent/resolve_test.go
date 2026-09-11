package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// v1Card is a v1.0 card whose interface URL deliberately lives on a
// different host, to pin the url-field-is-authoritative behavior. The
// name embeds JSON-escaped ANSI codes to exercise sanitization.
const v1Card = `{
  "name": "Esc\u001b[31mRed\u001b[0mAgent",
  "description": "Serves money facts",
  "version": "2.1.0",
  "provider": {"organization": "Acme"},
  "capabilities": {"streaming": true, "pushNotifications": false},
  "supportedInterfaces": [
    {"url": "https://grpc.example.com/api", "protocolBinding": "GRPC", "protocolVersion": "1.0"},
    {"url": "https://rpc.example.com/api", "protocolBinding": "JSONRPC", "protocolVersion": "1.0", "tenant": "acme"}
  ],
  "securitySchemes": {
    "bearer": {"httpAuthSecurityScheme": {"scheme": "Bearer", "bearerFormat": "JWT"}}
  },
  "defaultInputModes": ["text/plain"],
  "defaultOutputModes": ["text/plain"],
  "skills": [
    {"id": "s1", "name": "Refund", "description": "Issue refunds", "tags": ["money"], "inputModes": ["text/plain"]}
  ]
}`

// v03Card is a legacy 0.3 card whose url field points elsewhere.
const v03Card = `{
  "name": "Legacy Agent",
  "description": "Old but streaming",
  "url": "https://backend.example.com/a2a",
  "version": "1.0.0",
  "protocolVersion": "0.3.0",
  "provider": {"organization": "OldCorp"},
  "capabilities": {"streaming": true, "pushNotifications": true},
  "defaultInputModes": ["text"],
  "defaultOutputModes": ["text"],
  "skills": [
    {"id": "q", "name": "Query", "description": "Answer things", "tags": ["misc"]}
  ],
  "securitySchemes": {"key": {"type": "apiKey", "name": "X-Key", "in": "header"}},
  "security": [{"key": []}]
}`

// cardServer serves named well-known paths; a missing entry 404s.
func cardServer(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testClient() *http.Client { return &http.Client{Timeout: 5 * time.Second} }

func TestResolve(t *testing.T) {
	tests := []struct {
		name     string
		files    map[string]string
		input    func(srv string) string // build the resolve input
		mode     ProtocolMode
		wantWire string
		wantBase string
		wantName string
		wantErr  string // non-empty: expect error containing this
	}{
		{
			name:     "v1 only, auto",
			files:    map[string]string{"/.well-known/agent-card.json": v1Card},
			input:    func(srv string) string { return srv },
			mode:     Auto,
			wantWire: WireV1,
			wantBase: "https://rpc.example.com/api", // JSONRPC preferred over earlier GRPC
			wantName: "EscRedAgent",                 // ANSI stripped
		},
		{
			name:     "v1 via trailing slash",
			files:    map[string]string{"/.well-known/agent-card.json": v1Card},
			input:    func(srv string) string { return srv + "/" },
			mode:     Auto,
			wantWire: WireV1,
			wantBase: "https://rpc.example.com/api",
		},
		{
			name:     "v1 via full card url",
			files:    map[string]string{"/.well-known/agent-card.json": v1Card},
			input:    func(srv string) string { return srv + "/.well-known/agent-card.json" },
			mode:     Auto,
			wantWire: WireV1,
			wantBase: "https://rpc.example.com/api",
		},
		{
			name:     "v0.3 only, auto falls back",
			files:    map[string]string{"/.well-known/agent.json": v03Card},
			input:    func(srv string) string { return srv },
			mode:     Auto,
			wantWire: WireV03,
			wantBase: "https://backend.example.com/a2a", // card url is authoritative
			wantName: "Legacy Agent",
		},
		{
			name:     "both well-known, auto prefers v1",
			files:    map[string]string{"/.well-known/agent-card.json": v1Card, "/.well-known/agent.json": v03Card},
			input:    func(srv string) string { return srv },
			mode:     Auto,
			wantWire: WireV1,
			wantBase: "https://rpc.example.com/api",
		},
		{
			name:     "force 0.3 when both exist",
			files:    map[string]string{"/.well-known/agent-card.json": v1Card, "/.well-known/agent.json": v03Card},
			input:    func(srv string) string { return srv },
			mode:     Force3,
			wantWire: WireV03,
			wantBase: "https://backend.example.com/a2a",
		},
		{
			name:    "force 1.0 against 0.3-only server errors",
			files:   map[string]string{"/.well-known/agent.json": v03Card},
			input:   func(srv string) string { return srv },
			mode:    Force1,
			wantErr: "A2A 1.0 card",
		},
		{
			name:    "force 0.3 against 1.0-only server errors",
			files:   map[string]string{"/.well-known/agent-card.json": v1Card},
			input:   func(srv string) string { return srv },
			mode:    Force3,
			wantErr: "A2A 0.3 card",
		},
		{
			name:    "neither well-known errors clearly",
			files:   map[string]string{},
			input:   func(srv string) string { return srv },
			mode:    Auto,
			wantErr: "no agent card",
		},
		{
			name: "0.3 card served at the v1 path still resolves as 0.3",
			files: map[string]string{
				"/.well-known/agent-card.json": v03Card, // parses as SDK card, no 1.x interfaces
				"/.well-known/agent.json":      v03Card,
			},
			input:    func(srv string) string { return srv },
			mode:     Auto,
			wantWire: WireV03,
			wantBase: "https://backend.example.com/a2a",
		},
		{
			name: "garbage json at v1 path falls back to 0.3",
			files: map[string]string{
				"/.well-known/agent-card.json": "<html>not json</html>",
				"/.well-known/agent.json":      v03Card,
			},
			input:    func(srv string) string { return srv },
			mode:     Auto,
			wantWire: WireV03,
			wantBase: "https://backend.example.com/a2a",
		},
		{
			name: "0.3 card without url falls back to request base",
			files: map[string]string{
				"/.well-known/agent.json": `{"name": "N", "url": "", "protocolVersion": "0.3.0"}`,
			},
			input:    func(srv string) string { return srv },
			mode:     Auto,
			wantWire: WireV03,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := cardServer(t, tt.files)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			res, err := Resolve(ctx, testClient(), tt.input(srv.URL), tt.mode)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("Resolve() = %+v, want error containing %q", res, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Resolve() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}
			if res.Wire != tt.wantWire {
				t.Errorf("Wire = %q, want %q", res.Wire, tt.wantWire)
			}
			if tt.wantBase != "" && res.BaseURL != tt.wantBase {
				t.Errorf("BaseURL = %q, want %q", res.BaseURL, tt.wantBase)
			}
			if tt.wantName != "" && res.Summary.Name != tt.wantName {
				t.Errorf("Summary.Name = %q, want %q", res.Summary.Name, tt.wantName)
			}
			// Card pointer exclusivity.
			if res.Wire == WireV1 && (res.CardV1 == nil || res.CardV03 != nil) {
				t.Errorf("v1 resolve: CardV1=%v CardV03=%v, want card set / unset", res.CardV1, res.CardV03)
			}
			if res.Wire == WireV03 && (res.CardV03 == nil || res.CardV1 != nil) {
				t.Errorf("v0.3 resolve: CardV1=%v CardV03=%v, want card set / unset", res.CardV1, res.CardV03)
			}
		})
	}
}

func TestResolveSummaryMapping(t *testing.T) {
	t.Run("v1", func(t *testing.T) {
		srv := cardServer(t, map[string]string{"/.well-known/agent-card.json": v1Card})
		res, err := Resolve(context.Background(), testClient(), srv.URL, Auto)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		s := res.Summary
		if s.Provider != "Acme" || s.Version != "2.1.0" || s.ProtocolVersion != "1.0" {
			t.Errorf("header fields: %+v", s)
		}
		if !s.Capabilities.Streaming || s.Capabilities.PushNotifications {
			t.Errorf("capabilities: %+v", s.Capabilities)
		}
		if len(s.Interfaces) != 2 || s.Interfaces[1].Tenant != "acme" {
			t.Errorf("interfaces: %+v", s.Interfaces)
		}
		if len(s.SecuritySchemes) != 1 || s.SecuritySchemes[0].Type != "http" ||
			s.SecuritySchemes[0].Details["scheme"] != "Bearer" {
			t.Errorf("securitySchemes: %+v", s.SecuritySchemes)
		}
		if len(s.Skills) != 1 || s.Skills[0].ID != "s1" || s.Skills[0].Name != "Refund" {
			t.Errorf("skills: %+v", s.Skills)
		}
	})
	t.Run("v0.3", func(t *testing.T) {
		srv := cardServer(t, map[string]string{"/.well-known/agent.json": v03Card})
		res, err := Resolve(context.Background(), testClient(), srv.URL, Auto)
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		s := res.Summary
		if s.Provider != "OldCorp" || s.ProtocolVersion != "0.3.0" {
			t.Errorf("header fields: %+v", s)
		}
		if !s.Capabilities.Streaming || !s.Capabilities.PushNotifications {
			t.Errorf("capabilities: %+v", s.Capabilities)
		}
		if len(s.Interfaces) != 1 || s.Interfaces[0].URL != "https://backend.example.com/a2a" {
			t.Errorf("interfaces: %+v", s.Interfaces)
		}
		if len(s.SecuritySchemes) != 1 || s.SecuritySchemes[0].Type != "apiKey" ||
			s.SecuritySchemes[0].Details["name"] != "X-Key" {
			t.Errorf("securitySchemes: %+v", s.SecuritySchemes)
		}
	})
}

func TestResolveSizeCaps(t *testing.T) {
	// A 0.3 card with hostile content: ANSI codes, control characters,
	// an oversized description, and too many skills.
	var sb strings.Builder
	sb.WriteString(`{"name": "big", "url": "https://x.example.com", "protocolVersion": "0.3.0", "description": "`)
	sb.WriteString(strings.Repeat("\\u001b[31mA", 8000)) // 48KB of JSON-escaped ANSI
	sb.WriteString(`", "skills": [`)
	for i := 0; i < 300; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"name": "skill-%d"}`, i)
	}
	sb.WriteString(`]}`)
	srv := cardServer(t, map[string]string{"/.well-known/agent.json": sb.String()})

	res, err := Resolve(context.Background(), testClient(), srv.URL, Auto)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	s := res.Summary
	if len(s.Description) > maxString {
		t.Errorf("description len = %d, want <= %d", len(s.Description), maxString)
	}
	if strings.Contains(s.Description, "\x1b") {
		t.Error("description still contains ANSI escapes")
	}
	if len(s.Skills) != maxSkills {
		t.Errorf("skills = %d, want capped at %d", len(s.Skills), maxSkills)
	}
}

func TestResolveOversizedCardRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"name": "x", "protocolVersion": "0.3.0", "pad": "`)
		w.Write(make([]byte, maxCardBytes+1024))
		fmt.Fprint(w, `"}`)
	}))
	t.Cleanup(srv.Close)

	// Force 0.3 so the hand-rolled path (which enforces the cap) runs.
	if _, err := Resolve(context.Background(), testClient(), srv.URL, Force3); err == nil ||
		!strings.Contains(err.Error(), "1MB") {
		t.Fatalf("Resolve() oversize: err = %v, want exceeds 1MB", err)
	}
}

func TestNormalizeTarget(t *testing.T) {
	tests := []struct {
		in         string
		wantPrefix string
		wantV1     string
		wantV03    string
		wantFile   bool
		wantErr    bool
	}{
		{in: "thehiveryiq.com", wantPrefix: "https://thehiveryiq.com", wantV1: "https://thehiveryiq.com/.well-known/agent-card.json", wantV03: "https://thehiveryiq.com/.well-known/agent.json"},
		{in: "host.example.com:8080", wantPrefix: "https://host.example.com:8080", wantV1: "https://host.example.com:8080/.well-known/agent-card.json", wantV03: "https://host.example.com:8080/.well-known/agent.json"},
		{in: "http://localhost:8877/", wantPrefix: "http://localhost:8877", wantV1: "http://localhost:8877/.well-known/agent-card.json", wantV03: "http://localhost:8877/.well-known/agent.json"},
		{in: "https://a.example.com/base/", wantPrefix: "https://a.example.com/base", wantV1: "https://a.example.com/base/.well-known/agent-card.json", wantV03: "https://a.example.com/base/.well-known/agent.json"},
		{in: "https://a.example.com/.well-known/agent.json", wantPrefix: "https://a.example.com", wantV1: "https://a.example.com/.well-known/agent.json", wantV03: "https://a.example.com/.well-known/agent.json"},
		{in: "https://a.example.com/cards/agent-card.json?x=1", wantPrefix: "https://a.example.com/cards", wantV1: "https://a.example.com/cards/agent-card.json", wantV03: "https://a.example.com/cards/agent-card.json"},
		{in: "file:///tmp/card.json", wantPrefix: "file:///tmp/card.json", wantV1: "file:///tmp/card.json", wantV03: "file:///tmp/card.json", wantFile: true},
		{in: "ftp://x", wantErr: true},
		{in: "   ", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := normalizeTarget(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeTarget(%q) = %+v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeTarget(%q): %v", tt.in, err)
			}
			if got.prefix != tt.wantPrefix || got.cardV1 != tt.wantV1 || got.cardV03 != tt.wantV03 || got.file != tt.wantFile {
				t.Errorf("normalizeTarget(%q) = %+v\nwant prefix %q v1 %q v03 %q file %v",
					tt.in, got, tt.wantPrefix, tt.wantV1, tt.wantV03, tt.wantFile)
			}
		})
	}
}

func TestResolveFileURL(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/card.json"
	if err := os.WriteFile(path, []byte(v03Card), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	res, err := Resolve(context.Background(), testClient(), "file://"+path, Auto)
	if err != nil {
		t.Fatalf("Resolve file://: %v", err)
	}
	if res.Wire != WireV03 || res.Summary.Name != "Legacy Agent" {
		t.Errorf("file resolve: %+v", res.Summary)
	}
}

func TestParseProtocolMode(t *testing.T) {
	tests := []struct {
		in      string
		want    ProtocolMode
		wantErr bool
	}{
		{"", Auto, false}, {"auto", Auto, false}, {"AUTO", Auto, false},
		{"1.0", Force1, false}, {"1", Force1, false},
		{"0.3", Force3, false}, {"0.3.0", Force3, false},
		{"2.0", Auto, true}, {"x", Auto, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseProtocolMode(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseProtocolMode(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("ParseProtocolMode(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
			}
		})
	}
}
