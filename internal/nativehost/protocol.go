package nativehost

import (
	"context"
	"errors"
	"time"

	"github.com/rnsc/vaultr/internal/cache"
	"github.com/rnsc/vaultr/internal/index"
)

// Protocol is the message format version. The extension sends its own in
// hello; a different number is an error that tells the user what to
// update. Bump it when a message changes incompatibly.
const Protocol = 1

// Request is a message from the extension. Every request carries an ID
// (increasing, so a cancel can tell a queued request from a finished one),
// echoed in the response and in any events sent while it runs. Fields
// apply to the request types noted.
type Request struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`

	// hello
	Protocol int    `json:"protocol,omitempty"`
	Version  string `json:"version,omitempty"` // the extension's version

	// search, refresh, get. Namespace nil means the configured one, ""
	// the root namespace.
	Namespace     *string `json:"namespace,omitempty"`
	AllNamespaces bool    `json:"allNamespaces,omitempty"`
	Query         string  `json:"query,omitempty"`
	Limit         int     `json:"limit,omitempty"`
	Path          string  `json:"path,omitempty"`
	Key           string  `json:"key,omitempty"`

	// login: "oidc" or "token" ("" = the configured method).
	Method string `json:"method,omitempty"`
	Token  string `json:"token,omitempty"`

	// cancel: the ID of the request to stop.
	Target int64 `json:"target,omitempty"`
}

// Response answers one request: exactly one of Result and Error is set,
// unless Event is, which is an update sent while the request runs.
type Response struct {
	ID     int64  `json:"id"`
	Result any    `json:"result,omitempty"`
	Event  any    `json:"event,omitempty"`
	Error  *Error `json:"error,omitempty"`
}

// Error codes, for the extension to choose what to show.
const (
	CodeBadRequest  = "bad_request"
	CodeProtocol    = "protocol"      // extension and vaultr disagree on Protocol
	CodeNoConfig    = "no_config"     // no Vault address: config file missing or incomplete
	CodeConfig      = "config"        // the config file could not be read
	CodeNotLoggedIn = "not_logged_in" // token missing, expired or revoked
	CodeUseTerminal = "use_terminal"  // this login method needs `vaultr login`
	CodeForbidden   = "forbidden"
	CodeNotFound    = "not_found"
	CodeTooLarge    = "too_large"
	CodeCanceled    = "canceled"
	CodeOther       = "error"
)

// Error is a failed request.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Sentinel errors a Backend can return (or wrap) to pick an error code.
var (
	ErrNotLoggedIn = errors.New("not logged in to Vault (no token, or it expired or was revoked)")
	ErrUseTerminal = errors.New("this login method needs a terminal")
)

// HelloResult answers hello.
type HelloResult struct {
	Protocol    int    `json:"protocol"`
	Version     string `json:"version"`
	Addr        string `json:"addr,omitempty"`
	Namespace   string `json:"namespace"` // configured; "" = root
	ConfigPath  string `json:"configPath"`
	ConfigFound bool   `json:"configFound"`
	AuthMethod  string `json:"authMethod,omitempty"`
	// Problem is set when vaultr can't work as configured (no address,
	// unreadable config). Other requests fail with the same error.
	Problem  *Error   `json:"problem,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Status answers status.
type Status struct {
	LoggedIn       bool   `json:"loggedIn"`
	Reason         string `json:"reason,omitempty"` // why not
	TokenNamespace string `json:"tokenNamespace,omitempty"`
	// TTL is the token's remaining lifetime in seconds, 0 if it does not
	// expire or is not logged in.
	TTL int64 `json:"ttl,omitempty"`
	// AuthMethod is the configured login method ("" = unset), so the
	// popup offers the right login.
	AuthMethod string `json:"authMethod,omitempty"`
}

// NamespacesResult answers namespaces.
type NamespacesResult struct {
	Namespaces []string `json:"namespaces"` // "" = root
	Configured string   `json:"configured"`
}

// Item is one search result: a key of a secret, or a secret without keys.
// It never holds a value.
type Item struct {
	Namespace string `json:"namespace"`
	Path      string `json:"path"`
	Key       string `json:"key,omitempty"`
}

// SearchResult answers search.
type SearchResult struct {
	Items     []Item `json:"items"`
	Total     int    `json:"total"`             // matches before the limit
	Truncated bool   `json:"truncated"`         // Items is shorter than Total
	Recent    int    `json:"recent"`            // the first Recent items were used lately
	Built     int64  `json:"built,omitempty"`   // index creation, Unix seconds
	Expires   int64  `json:"expires,omitempty"` // index expiry, Unix seconds
	Warning   string `json:"warning,omitempty"`
}

// RefreshResult answers refresh.
type RefreshResult struct {
	Secrets int    `json:"secrets"`
	Warning string `json:"warning,omitempty"`
}

// GetResult answers get. It is the only message that carries a value.
type GetResult struct {
	Value string `json:"value"`
}

// LoginResult answers login.
type LoginResult struct {
	Summary string `json:"summary"`
	Warning string `json:"warning,omitempty"`
}

// ProgressEvent is sent while an index is built.
type ProgressEvent struct {
	Type    string `json:"type"` // "progress"
	Secrets int64  `json:"secrets"`
	Lists   int64  `json:"lists"`
}

// LoginURLEvent is sent when an OIDC login opens the browser, so the
// popup can show the address if no browser window appeared.
type LoginURLEvent struct {
	Type string `json:"type"` // "login_url"
	URL  string `json:"url"`
}

// Info describes the configuration, without contacting Vault.
type Info struct {
	ConfigPath  string
	ConfigFound bool
	ConfigError string // set when the config could not be loaded
	Addr        string
	AddrSource  string // "default" when neither the environment nor the config set it
	Namespace   string // configured; "" = root
	AuthMethod  string // configured [auth] method; "" = unset
}

// Backend is what the handler needs from vaultr. Namespaces are full paths,
// "" for the root.
type Backend interface {
	Info() Info
	// Reload re-reads the config file and ~/.vault-token, keeping a token
	// from a login in this process when nothing is saved.
	Reload()
	Status(ctx context.Context) (Status, error)
	Namespaces(ctx context.Context) ([]string, error)
	// Index loads namespace ns's index, or builds it when stale (always
	// with rebuild). The string is a warning.
	Index(ctx context.Context, ns string, rebuild bool, onProgress func(index.Progress)) ([]index.Entry, cache.Header, string, error)
	// AllNamespaces is Index over every namespace the token can use;
	// entries carry their namespace.
	AllNamespaces(ctx context.Context, rebuild bool, onProgress func(index.Progress)) ([]index.Entry, cache.Header, string, error)
	// Value reads one key of a secret, live.
	Value(ctx context.Context, ns, path, key string) (string, error)
	// Login logs in with method "oidc" (calling openURL with the address
	// to visit) or "token".
	Login(ctx context.Context, method, token string, openURL func(string)) (summary, warning string, err error)
	// Recent lists namespace ns's recently used rows ("path#key" or
	// "path"), newest first; AddRecent records one.
	Recent(ns string) []string
	AddRecent(ns, item string)
}

// now is swappable in tests.
var now = time.Now
