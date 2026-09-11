// Package agent resolves A2A agent cards and negotiates the wire
// protocol version (1.0 or 0.3) for connecting to an agent.
//
// This file family currently covers card resolution only; the AgentConn
// interface and session orchestration arrive with milestone M3.
package agent

import (
	"sort"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/charmbracelet/x/ansi"
)

// Display caps applied while summarizing a card. Cards come from
// untrusted servers: every string is stripped of ANSI and control
// characters, capped in length, and lists are truncated before anything
// reaches the terminal.
const (
	maxString     = 4096 // bytes per string field, rune-safe truncated
	maxSkills     = 200
	maxInterfaces = 32
	maxSchemes    = 64
	maxModes      = 32
	maxTags       = 32
)

// CardV03 is a lightweight decoding of the legacy (v0.3) agent card
// served at /.well-known/agent.json. Only fields a2a-tui displays are
// decoded; unknown fields are ignored.
type CardV03 struct {
	Name               string                       `json:"name"`
	Description        string                       `json:"description"`
	URL                string                       `json:"url"`
	Version            string                       `json:"version"`
	ProtocolVersion    string                       `json:"protocolVersion"`
	Provider           *ProviderV03                 `json:"provider,omitempty"`
	Capabilities       CapabilitiesV03              `json:"capabilities"`
	DefaultInputModes  []string                     `json:"defaultInputModes"`
	DefaultOutputModes []string                     `json:"defaultOutputModes"`
	Skills             []SkillV03                   `json:"skills"`
	SecuritySchemes    map[string]SecuritySchemeV03 `json:"securitySchemes,omitempty"`
	Security           []map[string][]string        `json:"security,omitempty"`
}

// ProviderV03 is the provider block of a v0.3 card.
type ProviderV03 struct {
	Organization string `json:"organization"`
	URL          string `json:"url"`
}

// CapabilitiesV03 is the capabilities block of a v0.3 card.
type CapabilitiesV03 struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
}

// SkillV03 is one skill in a v0.3 card. ID is optional in some early
// cards, hence the pointer.
type SkillV03 struct {
	ID          *string  `json:"id,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
	Examples    []string `json:"examples,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

// SecuritySchemeV03 is an OpenAPI-style security scheme entry from a
// v0.3 card; only scalar fields are kept for display.
type SecuritySchemeV03 struct {
	Type             string `json:"type"`
	Description      string `json:"description"`
	Name             string `json:"name"`   // apiKey: header/query/cookie name
	In               string `json:"in"`     // apiKey: location
	Scheme           string `json:"scheme"` // http: auth scheme
	BearerFormat     string `json:"bearerFormat"`
	OpenIDConnectURL string `json:"openIdConnectUrl"`
}

// CapabilitiesSummary reports the optional capabilities an agent
// declares.
type CapabilitiesSummary struct {
	Streaming         bool
	PushNotifications bool
}

// InterfaceSummary describes one transport endpoint from a card.
type InterfaceSummary struct {
	URL             string
	Binding         string // "JSONRPC", "GRPC", "HTTP+JSON", …
	ProtocolVersion string
	Tenant          string
}

// SecuritySchemeSummary describes one declared security scheme, with the
// scheme-specific fields flattened into Details.
type SecuritySchemeSummary struct {
	ID      string
	Type    string // "apiKey", "http", "oauth2", "openIdConnect", "mutualTLS"
	Details map[string]string
}

// SkillSummary describes one agent skill.
type SkillSummary struct {
	ID          string
	Name        string
	Description string
	Tags        []string
	InputModes  []string
	OutputModes []string
}

// CardSummary is a display-oriented, size-capped projection of an agent
// card, mapped from either the v1.0 or the v0.3 flavor.
type CardSummary struct {
	Name               string
	Description        string
	Provider           string
	Version            string
	ProtocolVersion    string
	Capabilities       CapabilitiesSummary
	Interfaces         []InterfaceSummary
	SecuritySchemes    []SecuritySchemeSummary
	Skills             []SkillSummary
	DefaultInputModes  []string
	DefaultOutputModes []string
}

// summarizeV1 maps a v1.0 SDK card into a CardSummary. iface is the
// selected transport interface (nil tolerated).
func summarizeV1(card *a2a.AgentCard, iface *a2a.AgentInterface) CardSummary {
	s := CardSummary{
		Name:            sanitize(card.Name),
		Description:     sanitize(card.Description),
		Version:         sanitize(card.Version),
		ProtocolVersion: WireV1,
		Capabilities: CapabilitiesSummary{
			Streaming:         card.Capabilities.Streaming,
			PushNotifications: card.Capabilities.PushNotifications,
		},
	}
	if card.Provider != nil {
		s.Provider = sanitize(card.Provider.Org)
	}
	if iface != nil && strings.HasPrefix(string(iface.ProtocolVersion), "1.") {
		s.ProtocolVersion = sanitize(string(iface.ProtocolVersion))
	}
	for _, ifc := range card.SupportedInterfaces {
		if ifc == nil || len(s.Interfaces) >= maxInterfaces {
			continue
		}
		s.Interfaces = append(s.Interfaces, InterfaceSummary{
			URL:             sanitize(ifc.URL),
			Binding:         sanitize(string(ifc.ProtocolBinding)),
			ProtocolVersion: sanitize(string(ifc.ProtocolVersion)),
			Tenant:          sanitize(ifc.Tenant),
		})
	}
	s.SecuritySchemes = summarizeSchemesV1(card.SecuritySchemes)
	for _, sk := range card.Skills {
		if len(s.Skills) >= maxSkills {
			break
		}
		s.Skills = append(s.Skills, SkillSummary{
			ID:          sanitize(sk.ID),
			Name:        sanitize(sk.Name),
			Description: sanitize(sk.Description),
			Tags:        sanitizeStrings(sk.Tags, maxTags),
			InputModes:  sanitizeStrings(sk.InputModes, maxModes),
			OutputModes: sanitizeStrings(sk.OutputModes, maxModes),
		})
	}
	s.DefaultInputModes = sanitizeStrings(card.DefaultInputModes, maxModes)
	s.DefaultOutputModes = sanitizeStrings(card.DefaultOutputModes, maxModes)
	return s
}

// summarizeSchemesV1 flattens the SDK's discriminated security scheme
// union into display rows, ordered by scheme id.
func summarizeSchemesV1(schemes a2a.NamedSecuritySchemes) []SecuritySchemeSummary {
	if len(schemes) == 0 {
		return nil
	}
	ids := make([]string, 0, len(schemes))
	for id := range schemes {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)

	out := make([]SecuritySchemeSummary, 0, len(ids))
	for _, id := range ids {
		if len(out) >= maxSchemes {
			break
		}
		row := SecuritySchemeSummary{ID: sanitize(id), Details: map[string]string{}}
		add := func(k, v string) {
			if v != "" {
				row.Details[k] = sanitize(v)
			}
		}
		switch v := schemes[a2a.SecuritySchemeName(id)].(type) {
		case a2a.APIKeySecurityScheme:
			row.Type = "apiKey"
			add("name", v.Name)
			add("in", string(v.Location))
			add("description", v.Description)
		case a2a.HTTPAuthSecurityScheme:
			row.Type = "http"
			add("scheme", v.Scheme)
			add("bearerFormat", v.BearerFormat)
			add("description", v.Description)
		case a2a.OAuth2SecurityScheme:
			row.Type = "oauth2"
			add("oauth2MetadataUrl", v.Oauth2MetadataURL)
			add("description", v.Description)
			add("tokenUrl", oauthFlowField(v.Flows, "tokenUrl"))
			add("authorizationUrl", oauthFlowField(v.Flows, "authorizationUrl"))
		case a2a.OpenIDConnectSecurityScheme:
			row.Type = "openIdConnect"
			add("openIdConnectUrl", v.OpenIDConnectURL)
			add("description", v.Description)
		case a2a.MutualTLSSecurityScheme:
			row.Type = "mutualTLS"
			add("description", v.Description)
		default:
			row.Type = "unknown"
		}
		out = append(out, row)
	}
	return out
}

// oauthFlowField pulls a display-useful field out of whichever OAuth
// flow variant is set.
func oauthFlowField(f a2a.OAuthFlows, field string) string {
	switch f := f.(type) {
	case a2a.AuthorizationCodeOAuthFlow:
		if field == "authorizationUrl" {
			return f.AuthorizationURL
		}
		return f.TokenURL
	case a2a.ClientCredentialsOAuthFlow:
		return f.TokenURL
	case a2a.PasswordOAuthFlow:
		return f.TokenURL
	case a2a.DeviceCodeOAuthFlow:
		return f.TokenURL
	case a2a.ImplicitOAuthFlow:
		if field == "authorizationUrl" {
			return f.AuthorizationURL
		}
		return ""
	default:
		return ""
	}
}

// summarizeV03 maps a v0.3 card into a CardSummary, synthesizing a
// single JSONRPC interface from the card url field.
func summarizeV03(card *CardV03) CardSummary {
	proto := sanitize(firstNonEmpty(card.ProtocolVersion, "0.3.0"))
	s := CardSummary{
		Name:            sanitize(card.Name),
		Description:     sanitize(card.Description),
		Version:         sanitize(card.Version),
		ProtocolVersion: proto,
		Capabilities: CapabilitiesSummary{
			Streaming:         card.Capabilities.Streaming,
			PushNotifications: card.Capabilities.PushNotifications,
		},
		Interfaces: []InterfaceSummary{{
			URL:             sanitizeURL(card.URL),
			Binding:         "JSONRPC",
			ProtocolVersion: proto,
		}},
	}
	if card.Provider != nil {
		s.Provider = sanitize(card.Provider.Organization)
	}
	if card.SecuritySchemes != nil {
		ids := make([]string, 0, len(card.SecuritySchemes))
		for id := range card.SecuritySchemes {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if len(s.SecuritySchemes) >= maxSchemes {
				break
			}
			sc := card.SecuritySchemes[id]
			row := SecuritySchemeSummary{ID: sanitize(id), Type: sanitize(sc.Type), Details: map[string]string{}}
			add := func(k, v string) {
				if v != "" {
					row.Details[k] = sanitize(v)
				}
			}
			add("name", sc.Name)
			add("in", sc.In)
			add("scheme", sc.Scheme)
			add("bearerFormat", sc.BearerFormat)
			add("openIdConnectUrl", sc.OpenIDConnectURL)
			add("description", sc.Description)
			s.SecuritySchemes = append(s.SecuritySchemes, row)
		}
	}
	for _, sk := range card.Skills {
		if len(s.Skills) >= maxSkills {
			break
		}
		sum := SkillSummary{
			Name:        sanitize(sk.Name),
			Description: sanitize(sk.Description),
			Tags:        sanitizeStrings(sk.Tags, maxTags),
			InputModes:  sanitizeStrings(sk.InputModes, maxModes),
			OutputModes: sanitizeStrings(sk.OutputModes, maxModes),
		}
		if sk.ID != nil {
			sum.ID = sanitize(*sk.ID)
		}
		s.Skills = append(s.Skills, sum)
	}
	s.DefaultInputModes = sanitizeStrings(card.DefaultInputModes, maxModes)
	s.DefaultOutputModes = sanitizeStrings(card.DefaultOutputModes, maxModes)
	return s
}

// sanitize strips ANSI escape sequences and terminal control characters,
// collapses whitespace, and caps the length of s. It is applied to every
// string that originates on an agent card.
func sanitize(s string) string {
	s = ansi.Strip(s)
	var b strings.Builder
	b.Grow(min(len(s), maxString))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			b.WriteByte(' ')
		case r < 0x20 || r == 0x7f:
			// drop other C0 controls and DEL
		case r >= 0x80 && r <= 0x9f:
			// drop C1 controls
		default:
			b.WriteRune(r)
		}
		if b.Len() >= maxString {
			break
		}
	}
	return truncateRunes(strings.Join(strings.Fields(b.String()), " "), maxString)
}

// sanitizeURL is sanitize without whitespace collapsing, for values used
// as URLs rather than prose.
func sanitizeURL(s string) string {
	s = ansi.Strip(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if (r >= 0x21 && r != 0x7f) || r > 0x9f {
			b.WriteRune(r)
		}
	}
	return truncateRunes(b.String(), maxString)
}

// sanitizeStrings sanitizes a list, drops empty entries, and caps its
// length.
func sanitizeStrings(in []string, limit int) []string {
	out := make([]string, 0, min(len(in), limit))
	for _, v := range in {
		if len(out) >= limit {
			break
		}
		if s := sanitize(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// truncateRunes cuts s to at most limit bytes on a rune boundary,
// appending an ellipsis when truncation happened.
func truncateRunes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	// Leave room for the ellipsis, then walk back to a rune boundary.
	cut := max(0, limit-len("…"))
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
