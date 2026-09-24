package testvault

import (
	"context"
	"fmt"
	"strings"
)

// EnableUserpass mounts a userpass auth method at mount in namespace ns
// and creates one user with the given policies.
func EnableUserpass(ctx context.Context, admin *Admin, ns, mount, user, password string, policies []string) error {
	a := admin.In(ns)
	if _, err := a.write(ctx, "sys/auth/"+mount, map[string]any{"type": "userpass"}); err != nil {
		return fmt.Errorf("enable userpass at %s: %w", mount, err)
	}
	_, err := a.write(ctx, "auth/"+mount+"/users/"+user, map[string]any{
		"password": password, "token_policies": strings.Join(policies, ","), "token_ttl": "1h",
	})
	return err
}

// EnableOIDC mounts an OIDC auth method pointing at a (fake) provider.
func EnableOIDC(ctx context.Context, admin *Admin, ns, mount, discoveryURL, clientID, clientSecret string) error {
	a := admin.In(ns)
	if _, err := a.write(ctx, "sys/auth/"+mount, map[string]any{"type": "oidc"}); err != nil {
		return fmt.Errorf("enable oidc at %s: %w", mount, err)
	}
	_, err := a.write(ctx, "auth/"+mount+"/config", map[string]any{
		"oidc_discovery_url": discoveryURL,
		"oidc_client_id":     clientID,
		"oidc_client_secret": clientSecret,
	})
	if err != nil {
		return fmt.Errorf("configure oidc: %w", err)
	}
	return nil
}

// OIDCRole creates an OIDC role allowing the given redirect URIs.
func OIDCRole(ctx context.Context, admin *Admin, ns, mount, role, clientID string, redirects, policies []string) error {
	_, err := admin.In(ns).write(ctx, "auth/"+mount+"/role/"+role, map[string]any{
		"role_type":             "oidc",
		"user_claim":            "sub",
		"bound_audiences":       []string{clientID},
		"allowed_redirect_uris": redirects,
		"token_policies":        policies,
		"token_ttl":             "1h",
	})
	return err
}

// DisableAuth removes an auth mount.
func DisableAuth(ctx context.Context, admin *Admin, ns, mount string) {
	_ = admin.In(ns).delete(ctx, "sys/auth/"+mount)
}
