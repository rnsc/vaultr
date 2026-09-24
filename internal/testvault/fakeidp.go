package testvault

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"time"
)

// FakeIDP is a minimal OpenID Connect provider for tests. Its authorize
// endpoint approves every request immediately (as if the user had already
// signed in) by redirecting back with a code; the token endpoint returns
// an RS256-signed ID token that Vault verifies against the JWKS.
type FakeIDP struct {
	URL          string
	ClientID     string
	ClientSecret string
	Subject      string

	srv   *httptest.Server
	key   *rsa.PrivateKey
	mu    sync.Mutex
	codes map[string]string // code -> nonce
	deny  map[string]bool   // redirect hosts ("localhost:port") to refuse
}

// DenyRedirect makes the provider refuse logins whose redirect URI has
// this host:port, as if the user clicked "deny".
func (p *FakeIDP) DenyRedirect(hostport string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deny[hostport] = true
}

// NewFakeIDP starts the provider.
func NewFakeIDP() (*FakeIDP, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	p := &FakeIDP{ClientID: "vaultr-test", ClientSecret: "s3cret", Subject: "alice", key: key, codes: map[string]string{}, deny: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", p.discovery)
	mux.HandleFunc("/keys", p.jwks)
	mux.HandleFunc("/authorize", p.authorize)
	mux.HandleFunc("/token", p.token)
	p.srv = httptest.NewServer(mux)
	p.URL = p.srv.URL
	return p, nil
}

// Close stops the provider.
func (p *FakeIDP) Close() { p.srv.Close() }

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (p *FakeIDP) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"issuer":                                p.URL,
		"authorization_endpoint":                p.URL + "/authorize",
		"token_endpoint":                        p.URL + "/token",
		"jwks_uri":                              p.URL + "/keys",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"scopes_supported":                      []string{"openid"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
	})
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (p *FakeIDP) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := p.key.PublicKey
	writeJSON(w, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "k1",
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

func (p *FakeIDP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	back, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || back.Host == "" {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	v := url.Values{"state": {q.Get("state")}}
	p.mu.Lock()
	deny := p.deny[back.Host]
	p.mu.Unlock()
	if deny {
		v.Set("error", "access_denied")
		v.Set("error_description", "the user declined")
	} else {
		code := b64(randBytesN(16))
		p.mu.Lock()
		p.codes[code] = q.Get("nonce")
		p.mu.Unlock()
		v.Set("code", code)
	}
	back.RawQuery = v.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

func (p *FakeIDP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	p.mu.Lock()
	nonce, ok := p.codes[r.Form.Get("code")]
	delete(p.codes, r.Form.Get("code"))
	p.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "invalid_grant"})
		return
	}
	now := time.Now()
	claims := map[string]any{
		"iss": p.URL, "sub": p.Subject, "aud": p.ClientID,
		"iat": now.Unix(), "exp": now.Add(10 * time.Minute).Unix(), "nonce": nonce,
	}
	writeJSON(w, map[string]any{
		"access_token": b64(randBytesN(16)), "token_type": "Bearer", "expires_in": 600,
		"id_token": p.sign(claims),
	})
}

func (p *FakeIDP) sign(claims map[string]any) string {
	h, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	c, _ := json.Marshal(claims)
	input := b64(h) + "." + b64(c)
	sum := sha256.Sum256([]byte(input))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
	return input + "." + b64(sig)
}

func randBytesN(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}
