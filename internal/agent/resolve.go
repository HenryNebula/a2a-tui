package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
)

// Wire protocol versions reported by Resolved.Wire.
const (
	WireV1  = "1.0"
	WireV03 = "0.3"
)

// ProtocolMode selects which wire protocol Resolve negotiates.
type ProtocolMode int

const (
	// Auto detects the protocol from the agent card (v1.0 preferred).
	Auto ProtocolMode = iota
	// Force1 requires a v1.0 agent card.
	Force1
	// Force3 requires a v0.3 agent card.
	Force3
)

// ParseProtocolMode converts a user-facing flag value into a
// ProtocolMode. Accepted: "", "auto", "1.0", "0.3".
func ParseProtocolMode(s string) (ProtocolMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return Auto, nil
	case "1.0", "1":
		return Force1, nil
	case "0.3", "0.3.0":
		return Force3, nil
	default:
		return Auto, fmt.Errorf("invalid protocol %q (want 1.0, 0.3 or auto)", s)
	}
}

// maxCardBytes caps card responses fetched by the hand-rolled 0.3 path.
const maxCardBytes = 1 << 20 // 1MB

// Resolved is the outcome of card resolution: which wire protocol the
// agent speaks, where to POST messages, and a display-ready summary.
type Resolved struct {
	// Wire is "1.0" or "0.3".
	Wire string
	// BaseURL is the POST target: for v1.0 the first JSONRPC interface
	// URL from the card; for v0.3 the card url field, falling back to
	// the request base. A card's url may differ from its host — it is
	// authoritative.
	BaseURL string
	// CardV1 is the parsed v1.0 card, nil when Wire is "0.3".
	CardV1 *a2a.AgentCard
	// CardV03 is the parsed v0.3 card, nil when Wire is "1.0".
	CardV03 *CardV03
	// Summary is the size-capped, sanitized display projection.
	Summary CardSummary
}

// Resolve fetches the agent card for baseOrURL and negotiates the wire
// protocol per mode.
//
// baseOrURL may be a bare host ("agent.example.com" → https://), a base
// URL (with or without trailing slash), a path-prefixed base, or a full
// card URL. file:// URLs load cards from the local filesystem.
//
// In Auto mode the v1.0 well-known path is tried first via the official
// SDK resolver; on 404, parse failure, or a card whose interfaces do not
// declare a 1.x protocol version, the legacy 0.3 well-known path is
// tried. When both fail the combined error is returned.
func Resolve(ctx context.Context, client *http.Client, baseOrURL string, mode ProtocolMode) (*Resolved, error) {
	t, err := normalizeTarget(baseOrURL)
	if err != nil {
		return nil, err
	}

	var err1 error
	if mode != Force3 {
		res, err := resolveV1(ctx, client, t)
		if err == nil {
			return res, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if mode == Force1 {
			return nil, fmt.Errorf("A2A 1.0 card: %w", err)
		}
		err1 = err
	}

	res, err3 := resolveV03(ctx, client, t)
	if err3 == nil {
		return res, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if mode == Force3 {
		return nil, fmt.Errorf("A2A 0.3 card: %w", err3)
	}
	return nil, fmt.Errorf("no agent card at %s (A2A 1.0: %v; A2A 0.3: %v)", t.prefix, err1, err3)
}

// target is a normalized resolution request: the URLs to try for each
// protocol flavor plus the fallback POST base.
type target struct {
	input   string // normalized input (scheme added, query/fragment kept)
	prefix  string // scheme://host[:port][/path-prefix], no trailing slash
	cardV1  string // exact URL of the v1.0 card attempt
	cardV03 string // exact URL of the v0.3 card attempt
	file    bool   // file:// input: cardV1 == cardV03 == input
}

// normalizeTarget accepts a bare host, base URL, path-prefixed base, or
// full card URL and derives the per-protocol card URLs.
func normalizeTarget(input string) (*target, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return nil, errors.New("empty agent URL")
	}
	if !strings.Contains(s, "://") {
		s = "https://" + s // bare host ("agent.example.com", "host:8080")
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("parse agent URL: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
		if u.Host == "" {
			return nil, fmt.Errorf("agent URL %q has no host", s)
		}
	case "file":
		// The SDK resolver reads file URLs directly; use it for both
		// attempts so local fixtures work in either flavor.
		return &target{input: s, prefix: s, cardV1: s, cardV03: s, file: true}, nil
	default:
		return nil, fmt.Errorf("unsupported scheme %q (want http, https or file)", u.Scheme)
	}

	origin := u.Scheme + "://" + u.Host
	t := &target{input: s, prefix: origin}
	path := strings.TrimSuffix(u.Path, "/")

	switch {
	case path == "" || path == "/":
		t.cardV1 = origin + "/.well-known/agent-card.json"
		t.cardV03 = origin + "/.well-known/agent.json"
	case strings.HasSuffix(path, "/agent-card.json") || strings.HasSuffix(path, "/agent.json"):
		// A full card URL: fetch it directly for both attempts. The
		// prefix is its directory, except that the standard well-known
		// directory maps back to the origin root.
		t.cardV1 = stripQueryFrag(s)
		t.cardV03 = stripQueryFrag(s)
		dir := path[:strings.LastIndex(path, "/")]
		if dir == "/.well-known" {
			dir = ""
		}
		t.prefix = origin + dir
	default:
		// A path-prefixed base (reverse proxy): well-known files hang
		// off the prefix.
		t.prefix = origin + path
		t.cardV1 = t.prefix + "/.well-known/agent-card.json"
		t.cardV03 = t.prefix + "/.well-known/agent.json"
	}
	return t, nil
}

func stripQueryFrag(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		return s[:i]
	}
	return s
}

// resolveV1 fetches and validates a v1.0 card via the official SDK
// resolver (DefaultResolver when client is nil).
func resolveV1(ctx context.Context, client *http.Client, t *target) (*Resolved, error) {
	resolver := &agentcard.Resolver{Client: client}
	if client == nil {
		resolver = agentcard.DefaultResolver
	}
	card, err := resolver.Resolve(ctx, t.cardV1)
	if err != nil {
		return nil, err
	}
	iface := selectInterface(card)
	if iface == nil {
		return nil, fmt.Errorf("%s has no A2A 1.x interface", t.cardV1)
	}
	base := sanitizeURL(iface.URL)
	if !isAbsoluteHTTP(base) {
		base = t.prefix // defensive: malformed interface URL
	}
	return &Resolved{
		Wire:    WireV1,
		BaseURL: base,
		CardV1:  card,
		Summary: summarizeV1(card, iface),
	}, nil
}

// selectInterface picks the POST target interface: the first interface
// declaring a 1.x protocol version, preferring the JSONRPC binding.
func selectInterface(card *a2a.AgentCard) *a2a.AgentInterface {
	var fallback *a2a.AgentInterface
	for _, iface := range card.SupportedInterfaces {
		if iface == nil || !strings.HasPrefix(string(iface.ProtocolVersion), "1.") {
			continue
		}
		if iface.ProtocolBinding == a2a.TransportProtocolJSONRPC {
			return iface
		}
		if fallback == nil {
			fallback = iface
		}
	}
	return fallback
}

// resolveV03 fetches the legacy 0.3 card with a size-capped request and
// validates that it plausibly is one.
func resolveV03(ctx context.Context, client *http.Client, t *target) (*Resolved, error) {
	body, err := fetchCard(ctx, client, t.cardV03)
	if err != nil {
		return nil, err
	}
	var card CardV03
	if err := json.Unmarshal(body, &card); err != nil {
		return nil, fmt.Errorf("parse %s: %w", t.cardV03, err)
	}
	if card.Name == "" {
		return nil, fmt.Errorf("%s is not a v0.3 agent card (no name)", t.cardV03)
	}
	if card.ProtocolVersion != "" && !strings.HasPrefix(card.ProtocolVersion, "0.") {
		return nil, fmt.Errorf("%s serves protocol %q, not 0.3", t.cardV03, card.ProtocolVersion)
	}
	base := sanitizeURL(card.URL)
	if !isAbsoluteHTTP(base) {
		base = t.prefix
	}
	return &Resolved{
		Wire:    WireV03,
		BaseURL: base,
		CardV03: &card,
		Summary: summarizeV03(&card),
	}, nil
}

// fetchCard GETs url (or reads it, for file://) with a 1MB response cap.
func fetchCard(ctx context.Context, client *http.Client, rawURL string) ([]byte, error) {
	if strings.HasPrefix(rawURL, "file://") {
		u, err := url.Parse(rawURL)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", rawURL, err)
		}
		path := u.Path
		if path == "" {
			path = u.Opaque
		}
		if path == "" {
			return nil, fmt.Errorf("file URL %q has no path", rawURL)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rawURL, err)
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxCardBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", rawURL, err)
		}
		if len(data) > maxCardBytes {
			return nil, fmt.Errorf("card at %s exceeds 1MB", rawURL)
		}
		return data, nil
	}

	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", rawURL, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCardBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rawURL, err)
	}
	if len(data) > maxCardBytes {
		return nil, fmt.Errorf("card at %s exceeds 1MB", rawURL)
	}
	return data, nil
}

// isAbsoluteHTTP reports whether s is an absolute http(s) URL.
func isAbsoluteHTTP(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
