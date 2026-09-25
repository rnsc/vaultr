package search

import (
	"testing"

	"github.com/rnsc/vaultr/internal/index"
)

func entries() []index.Entry {
	return []index.Entry{
		{Path: "secret/prod/payments/stripe", Mount: "secret/", KV: 2, Keys: []string{"api_key", "webhook_secret"}},
		{Path: "secret/prod/db", Mount: "secret/", KV: 2, Keys: []string{"password", "username"}},
		{Path: "secret/dev/password-reset", Mount: "secret/", KV: 2, Keys: []string{"smtp"}},
		{Path: "kv/legacy/ldap", Mount: "kv/", KV: 1},
	}
}

func keys(rows []Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Entry.Path+"#"+r.Key)
	}
	return out
}

func TestSearch(t *testing.T) {
	ix := New(entries())
	cases := []struct {
		q    string
		want []string
	}{
		{"STRIPE api", []string{"secret/prod/payments/stripe#api_key"}},
		{"k:password", []string{"secret/prod/db#password"}},
		{"password", []string{"secret/prod/db#password", "secret/dev/password-reset#smtp"}},
		{"p:password", []string{"secret/dev/password-reset#smtp"}},
		{"ldap", []string{"kv/legacy/ldap#"}},
		{"prod nomatch", nil},
	}
	for _, c := range cases {
		got := keys(ix.Search(c.q, 0))
		if len(got) != len(c.want) {
			t.Errorf("%q: got %v, want %v", c.q, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%q: got %v, want %v", c.q, got, c.want)
				break
			}
		}
	}
	if n := len(ix.Search("", 0)); n != 6 {
		t.Errorf("empty query returned %d rows, want 6", n)
	}
	if n := len(ix.Search("secret", 2)); n != 2 {
		t.Errorf("limit not applied: %d", n)
	}
}

func TestNamespaceIsPartOfThePath(t *testing.T) {
	ix := New([]index.Entry{
		{Path: "secret/app/db", Keys: []string{"password"}, Namespace: "team-a"},
		{Path: "secret/app/db", Keys: []string{"password"}, Namespace: "team-b"},
		{Path: "secret/app/db", Keys: []string{"password"}}, // the root
	})
	if got := ix.Search("team-a db", 0); len(got) != 1 || got[0].Entry.Namespace != "team-a" {
		t.Errorf("team-a db: %+v", got)
	}
	if got := ix.Search("p:team-b", 0); len(got) != 1 {
		t.Errorf("p:team-b: %+v", got)
	}
	if got := ix.Search("k:team", 0); len(got) != 0 {
		t.Errorf("k: must not match namespaces: %+v", got)
	}
	if got := ix.Search("db password", 0); len(got) != 3 {
		t.Errorf("all three namespaces: %d rows", len(got))
	}
}
