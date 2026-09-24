// Command seed fills a Vault dev server with the integration test fixture
// so vaultr can be tried by hand, or with -demo, the small fake tree used
// for the README GIFs:
//
//	scripts/vault-dev.sh start
//	eval "$(scripts/vault-dev.sh env)"
//	go run ./tools/seed [-demo]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/rnsc/vaultr/internal/testvault"
)

func main() {
	prefix := flag.String("prefix", "demo", "mount name prefix")
	demo := flag.Bool("demo", false, "seed the README demo tree into secret/ and kv-legacy/ instead")
	flag.Parse()
	addr, tok := os.Getenv("VAULT_ADDR"), os.Getenv("VAULT_TOKEN")
	if addr == "" || tok == "" {
		fmt.Fprintln(os.Stderr, "seed: set VAULT_ADDR and a root VAULT_TOKEN")
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if *demo {
		if err := testvault.SeedDemo(ctx, &testvault.Admin{Addr: addr, Token: tok}); err != nil {
			fmt.Fprintln(os.Stderr, "seed:", err)
			os.Exit(1)
		}
		fmt.Printf("seeded %d demo secrets\n", len(testvault.DemoSecrets))
		return
	}
	fx, err := testvault.Provision(ctx, &testvault.Admin{Addr: addr, Token: tok}, *prefix)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	reader, err := fx.Token(ctx, testvault.TokenOptions{Policies: []string{fx.Reader}, TTL: 2 * time.Hour})
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	fmt.Printf("seeded %d secrets into %s, %s and %s\n", len(fx.Secrets), fx.KV2, fx.KV1, fx.Empty)
	fmt.Printf("restricted token (policy %s, 2h): %s\n", fx.Reader, reader)
	fmt.Println("try: go run . stripe")
}
