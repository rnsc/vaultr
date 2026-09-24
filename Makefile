VAULT_VERSION ?= latest
export VAULT_DEV_PORT ?= 8200
VAULT_ENV = VAULT_ADDR=http://127.0.0.1:$(VAULT_DEV_PORT) VAULT_TOKEN=root

.PHONY: build test lint integration dev-vault stop-vault demo

build:
	go build -o vaultr .

lint:
	test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)
	go vet ./...
	go vet -tags integration ./...

test:
	go test -race ./...

# Starts a throwaway Vault dev server, runs the integration suite, stops it.
integration:
	scripts/vault-dev.sh start $(VAULT_VERSION)
	$(VAULT_ENV) go test -tags integration -race -count=1 ./integration; \
		status=$$?; scripts/vault-dev.sh stop; exit $$status

# Starts a dev server with the fixture tree loaded, for manual testing.
dev-vault:
	scripts/vault-dev.sh start $(VAULT_VERSION)
	$(VAULT_ENV) go run ./tools/seed
	@echo 'run: eval "$$(scripts/vault-dev.sh env)"'

stop-vault:
	scripts/vault-dev.sh stop

# Records demo/*.gif (needs vhs, ttyd, ffmpeg); CI does this on PRs.
demo:
	scripts/demo.sh
