package testvault

import (
	"context"
	"fmt"
)

// DemoSecrets is the fake tree shown in the README GIFs, in the dev
// server's default secret/ mount (KV v2) and a kv-legacy/ mount (KV v1).
var DemoSecrets = []Secret{
	{Mount: "secret/", Path: "prod/payments/stripe", Data: map[string]any{"api_key": "sk_live_demo_4f9a1c", "webhook_secret": "whsec_demo_77e0"}},
	{Mount: "secret/", Path: "prod/payments/adyen", Data: map[string]any{"api_key": "AQE_demo_31b2", "hmac_key": "demo-hmac-8c1d"}},
	{Mount: "secret/", Path: "prod/db/orders", Data: map[string]any{"host": "orders.db.prod.internal", "username": "orders_app", "password": "demo-Orders-9x2!"}},
	{Mount: "secret/", Path: "prod/db/users", Data: map[string]any{"host": "users.db.prod.internal", "username": "users_app", "password": "demo-Users-k3p?"}},
	{Mount: "secret/", Path: "prod/cache/redis", Data: map[string]any{"url": "rediss://cache.prod.internal:6380", "password": "demo-redis-51aa"}},
	{Mount: "secret/", Path: "staging/db/orders", Data: map[string]any{"host": "orders.db.staging.internal", "username": "orders_app", "password": "demo-staging-orders"}},
	{Mount: "secret/", Path: "staging/payments/stripe", Data: map[string]any{"api_key": "sk_test_demo_0b7e"}},
	{Mount: "secret/", Path: "teams/platform/github-bot", Data: map[string]any{"app_id": "123456", "private_key": "-----BEGIN DEMO KEY-----"}},
	{Mount: "secret/", Path: "teams/platform/pagerduty", Data: map[string]any{"routing_key": "demo-pd-6f3e"}},
	{Mount: "secret/", Path: "teams/data/snowflake", Data: map[string]any{"account": "acme-demo", "user": "etl", "password": "demo-snow-2c9d"}},
	{Mount: "secret/", Path: "teams/data/airflow/connections", Data: map[string]any{"postgres_uri": "postgres://demo@db/airflow", "s3_access_key": "AKIADEMO123", "s3_secret_key": "demo/s3/secret"}},
	{Mount: "secret/", Path: "ci/deploy/aws", Data: map[string]any{"access_key_id": "AKIADEMODEPLOY", "secret_access_key": "demo/deploy/secret"}},
	{Mount: "secret/", Path: "ci/deploy/docker-hub", Data: map[string]any{"username": "acme-ci", "token": "dckr_pat_demo"}},
	{Mount: "kv-legacy/", Path: "ldap/bind", Data: map[string]any{"bind_dn": "cn=svc-vault,dc=acme,dc=example", "bind_password": "demo-ldap-bind"}},
	{Mount: "kv-legacy/", Path: "smtp/relay", Data: map[string]any{"host": "smtp.acme.example", "user": "relay", "password": "demo-smtp-pass"}},
}

// SeedDemo writes DemoSecrets into a dev server (which already has secret/
// as KV v2).
func SeedDemo(ctx context.Context, admin *Admin) error {
	if _, err := admin.write(ctx, "sys/mounts/kv-legacy", map[string]any{"type": "kv", "options": map[string]string{"version": "1"}}); err != nil {
		return fmt.Errorf("mount kv-legacy: %w", err)
	}
	f := &Fixture{admin: admin, KV1: "kv-legacy/"}
	for _, s := range DemoSecrets {
		if err := f.write(ctx, s); err != nil {
			return err
		}
	}
	return nil
}
