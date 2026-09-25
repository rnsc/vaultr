// Package cache persists the index (paths and key names, never values) in
// an encrypted, self-expiring file.
//
// Encryption key = HKDF-SHA256(token || dek, salt), where dek is 32 random
// bytes stored in the token's own Vault cubbyhole. Cubbyhole storage is
// destroyed by Vault when the token expires or is revoked, so once that
// happens the cache can no longer be decrypted, even by someone who kept a
// copy of the file and of the old token. On top of that, the file carries
// an authenticated expiry (at most 2h, and never past the token's own
// expiry) which is checked against the Vault server's clock, not the local
// one; expired caches are deleted on sight.
package cache

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/rnsc/vaultr/internal/index"
	"github.com/rnsc/vaultr/internal/vault"
)

// MaxAge is the hard upper bound for a cache's lifetime.
const MaxAge = 2 * time.Hour

const (
	magic         = "VLTRC1\n"
	formatVersion = 1
	cubbyPrefix   = "cubbyhole/vaultr/"
)

// ErrStale means the cache is missing, expired, or unreadable and must be
// rebuilt. The wrapped error says why.
var ErrStale = errors.New("cache needs rebuild")

// Header is stored in clear text but authenticated.
type Header struct {
	Version   int       `json:"version"`
	Addr      string    `json:"addr"`
	Namespace string    `json:"namespace,omitempty"`
	Created   time.Time `json:"created"`
	Expires   time.Time `json:"expires"`
	Salt      []byte    `json:"salt"`
	// KeyRef names the cubbyhole entry holding the data key. Empty when the
	// token could not write to its cubbyhole (token-only mode).
	KeyRef string `json:"key_ref,omitempty"`
	// TokenNamespace is where the token (and so the cubbyhole) lives.
	TokenNamespace string `json:"token_namespace,omitempty"`
	// Owner identifies who the index was built for (a hash of the token's
	// entity ID), so a later login by the same person can keep it. Empty
	// for tokens without an identity.
	Owner string `json:"owner,omitempty"`
}

// ownerOf hashes an entity ID for the header, which is stored in clear.
func ownerOf(entityID string) string {
	if entityID == "" {
		return ""
	}
	h := sha256.Sum256([]byte("vaultr owner\x00" + entityID))
	return hex.EncodeToString(h[:16])
}

// Bound reports whether the cache is bound to server-side key material.
func (h Header) Bound() bool { return h.KeyRef != "" }

// Store reads and writes the cache file for one Vault client.
type Store struct {
	Dir    string
	Client *vault.Client
}

// File is the cache file path, one per Vault address and namespace.
func (s *Store) File() string {
	h := sha256.Sum256([]byte(s.Client.Addr + "\x00" + s.Client.Namespace))
	return filepath.Join(s.Dir, hex.EncodeToString(h[:8])+".cache")
}

// Save encrypts and writes entries. It returns the written header and a
// non-fatal warning (e.g. cubbyhole unavailable).
func (s *Store) Save(ctx context.Context, entries []index.Entry, maxAge time.Duration) (Header, string, error) {
	if maxAge <= 0 || maxAge > MaxAge {
		maxAge = MaxAge
	}
	ti, err := s.Client.LookupSelf(ctx)
	if err != nil {
		return Header{}, "", err
	}
	h := Header{
		Version:   formatVersion,
		Addr:      s.Client.Addr,
		Namespace: s.Client.Namespace,
		Created:   ti.ServerTime.UTC(),
		Expires:   ti.ServerTime.Add(maxAge).UTC(),
		Salt:      randBytes(32),

		TokenNamespace: ti.Namespace,
		Owner:          ownerOf(ti.EntityID),
	}
	if !ti.ExpireTime.IsZero() && ti.ExpireTime.Before(h.Expires) {
		h.Expires = ti.ExpireTime.UTC()
	}

	// Drop the previous data key, if any.
	if old, err := s.peek(); err == nil && old.KeyRef != "" {
		_ = s.Client.TokenDelete(ctx, cubbyPrefix+old.KeyRef)
	}

	var warning string
	dek := randBytes(32)
	ref := hex.EncodeToString(randBytes(16))
	_, err = s.Client.TokenWrite(ctx, cubbyPrefix+ref, map[string]string{
		"k":       base64.StdEncoding.EncodeToString(dek),
		"expires": h.Expires.Format(time.RFC3339),
	})
	if err != nil {
		dek = nil
		warning = "could not store the cache key in the token cubbyhole (" + err.Error() +
			"); the cache is protected by the token and local expiry only"
	} else {
		h.KeyRef = ref
	}

	hdr, err := json.Marshal(h)
	if err != nil {
		return Header{}, "", err
	}
	var plain bytes.Buffer
	zw := gzip.NewWriter(&plain)
	if err := json.NewEncoder(zw).Encode(entries); err != nil {
		return Header{}, "", err
	}
	if err := zw.Close(); err != nil {
		return Header{}, "", err
	}

	aead, err := s.aead(h, dek)
	if err != nil {
		return Header{}, "", err
	}
	nonce := randBytes(aead.NonceSize())
	aad := append([]byte(magic), hdr...)
	ct := aead.Seal(nil, nonce, plain.Bytes(), aad)

	var out bytes.Buffer
	out.WriteString(magic)
	_ = binary.Write(&out, binary.BigEndian, uint32(len(hdr)))
	out.Write(hdr)
	out.Write(nonce)
	out.Write(ct)
	if err := writeAtomic(s.File(), out.Bytes()); err != nil {
		return Header{}, "", err
	}
	return h, warning, nil
}

// Adopt saves entries, built with an earlier token, under the current one
// when both belong to the same identity (prev is the earlier header): the
// same person logging in again keeps their index without a re-crawl. It
// reports false, and saves nothing, when the identities differ or are
// unknown.
func (s *Store) Adopt(ctx context.Context, entries []index.Entry, prev Header, maxAge time.Duration) (Header, bool, string, error) {
	if prev.Owner == "" || prev.Addr != s.Client.Addr || prev.Namespace != s.Client.Namespace {
		return Header{}, false, "", nil
	}
	ti, err := s.Client.LookupSelf(ctx)
	if err != nil {
		return Header{}, false, "", err
	}
	if ownerOf(ti.EntityID) != prev.Owner {
		return Header{}, false, "", nil
	}
	h, warn, err := s.Save(ctx, entries, maxAge)
	return h, err == nil, warn, err
}

// Load decrypts the cache. Any error wrapping ErrStale means a rebuild is
// required; stale files are removed.
func (s *Store) Load(ctx context.Context) ([]index.Entry, Header, error) {
	raw, err := os.ReadFile(s.File())
	if errors.Is(err, os.ErrNotExist) {
		return nil, Header{}, fmt.Errorf("%w: no cache yet", ErrStale)
	}
	if err != nil {
		return nil, Header{}, err
	}
	h, hdr, body, err := parse(raw)
	if err != nil {
		s.removeFile()
		return nil, Header{}, fmt.Errorf("%w: %v", ErrStale, err)
	}
	if h.Addr != s.Client.Addr || h.Namespace != s.Client.Namespace {
		return nil, h, fmt.Errorf("%w: cache belongs to another server", ErrStale)
	}

	var dek []byte
	var now time.Time
	s.Client.HintTokenNamespace(h.TokenNamespace)
	if h.KeyRef != "" {
		resp, err := s.Client.TokenRead(ctx, cubbyPrefix+h.KeyRef)
		switch {
		case errors.Is(err, vault.ErrNotFound):
			s.removeFile()
			return nil, h, fmt.Errorf("%w: cache key is gone (token expired, revoked or replaced)", ErrStale)
		case errors.Is(err, vault.ErrForbidden):
			s.removeFile()
			return nil, h, fmt.Errorf("%w: token cannot read the cache key (new or invalid token)", ErrStale)
		case err != nil:
			return nil, h, err
		}
		var d struct {
			K string `json:"k"`
		}
		if err := json.Unmarshal(resp.Data, &d); err != nil {
			return nil, h, err
		}
		if dek, err = base64.StdEncoding.DecodeString(d.K); err != nil {
			return nil, h, fmt.Errorf("%w: malformed cache key", ErrStale)
		}
		now = resp.ServerTime
	} else {
		ti, err := s.Client.LookupSelf(ctx)
		if err != nil {
			return nil, h, err
		}
		now = ti.ServerTime
	}
	if now.IsZero() {
		now = time.Now()
	}
	if !now.Before(h.Expires) {
		s.destroy(ctx, h)
		return nil, h, fmt.Errorf("%w: cache expired at %s", ErrStale, h.Expires.Local().Format(time.Kitchen))
	}

	aead, err := s.aead(h, dek)
	if err != nil {
		return nil, h, err
	}
	ns := aead.NonceSize()
	if len(body) < ns {
		s.removeFile()
		return nil, h, fmt.Errorf("%w: truncated cache", ErrStale)
	}
	aad := append([]byte(magic), hdr...)
	plain, err := aead.Open(nil, body[:ns], body[ns:], aad)
	if err != nil {
		// Wrong token, or tampered file.
		s.removeFile()
		return nil, h, fmt.Errorf("%w: cache cannot be decrypted with the current token", ErrStale)
	}
	zr, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return nil, h, err
	}
	var entries []index.Entry
	if err := json.NewDecoder(zr).Decode(&entries); err != nil {
		return nil, h, err
	}
	return entries, h, nil
}

// Status returns the header without decrypting (for display only).
func (s *Store) Status() (Header, error) { return s.peek() }

// Purge deletes the local file and the cubbyhole key.
func (s *Store) Purge(ctx context.Context) error {
	h, err := s.peek()
	if err == nil {
		s.destroy(ctx, h)
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	s.removeFile()
	return nil
}

func (s *Store) destroy(ctx context.Context, h Header) {
	if h.KeyRef != "" {
		_ = s.Client.TokenDelete(ctx, cubbyPrefix+h.KeyRef)
	}
	s.removeFile()
}

func (s *Store) removeFile() { _ = os.Remove(s.File()) }

func (s *Store) peek() (Header, error) {
	raw, err := os.ReadFile(s.File())
	if err != nil {
		return Header{}, err
	}
	h, _, _, err := parse(raw)
	return h, err
}

func (s *Store) aead(h Header, dek []byte) (cipher.AEAD, error) {
	ikm := append([]byte(s.Client.Token()), dek...)
	info := fmt.Sprintf("vaultr cache v%d|%s|%s", h.Version, h.Addr, h.Namespace)
	key, err := hkdf.Key(sha256.New, ikm, h.Salt, info, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func parse(raw []byte) (Header, []byte, []byte, error) {
	var h Header
	if len(raw) < len(magic)+4 || string(raw[:len(magic)]) != magic {
		return h, nil, nil, errors.New("not a vaultr cache file")
	}
	rest := raw[len(magic):]
	n := binary.BigEndian.Uint32(rest[:4])
	rest = rest[4:]
	if uint64(n) > uint64(len(rest)) {
		return h, nil, nil, errors.New("corrupt cache header")
	}
	hdr := rest[:n]
	if err := json.Unmarshal(hdr, &h); err != nil {
		return h, nil, nil, fmt.Errorf("corrupt cache header: %w", err)
	}
	if h.Version != formatVersion {
		return h, nil, nil, fmt.Errorf("unsupported cache version %d", h.Version)
	}
	return h, hdr, rest[n:], nil
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".vaultr-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		panic(err)
	}
	return b
}
