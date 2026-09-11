// Package compat03 implements the client for the legacy A2A 0.3 wire
// protocol (JSON-RPC 2.0 over HTTP with SSE streaming, camelCase methods
// like "message/send", no A2A-Version header).
//
// It is a hand-rolled client: the official SDK only speaks 1.x, so every
// 0.3 wire struct, translation, and SSE frame lives in this package. The
// public surface mirrors connv1.Conn — method-shaped functions over SDK
// a2a types — so internal/agent can adapt it to agent.AgentConn with a
// thin wrapper. This package must not import internal/agent (cycle).
package compat03

// CardV03 is a lightweight decoding of the legacy (v0.3) agent card
// served at /.well-known/agent.json. Only fields a2a-tui displays (plus
// the ones the client needs) are decoded; unknown fields are ignored.
//
// The type lives here (not in internal/agent) so the compat client can
// hold the card it was built from; internal/agent aliases it.
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
