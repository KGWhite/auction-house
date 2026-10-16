# Everything runs in Docker; the host only needs Docker (with Compose v2) and make.
DC    := docker compose
GO    := $(DC) run --rm go
TOOLS := $(DC) run --rm tools

.PHONY: up down clean logs ps migrate migrate-down sqlc seed server worker web web-dev web-test \
        test test-race test-tc demo-race reconcile k6-smoke

up: ## build images and start PostgreSQL, migrations, server, worker and web (http://localhost:5173)
	$(DC) up -d --build --wait

down: ## stop everything (data is kept)
	$(DC) down

clean: ## stop everything and delete the database volume
	$(DC) --profile tools --profile dev --profile bench down -v --remove-orphans

logs: ## follow the logs of the running services
	$(DC) logs -f --tail=100

ps: ## show service status
	$(DC) ps

migrate: ## apply all migrations
	$(TOOLS) migrate

migrate-down: ## roll back one migration
	$(TOOLS) migrate down

sqlc: ## regenerate internal/db from queries/ and migrations/
	$(DC) run --rm sqlc generate

seed: ## insert players, item templates and the first auction round
	$(TOOLS) seed

server: ## run the API server in the foreground
	$(DC) up --build server

worker: ## run the settlement + refresh worker in the foreground
	$(DC) up --build worker

web: ## run the web container (nginx serving the production build) in the foreground
	$(DC) up --build web

web-dev: ## Vite dev server with hot reload on http://localhost:5174 (API proxied to the server service)
	$(DC) run --rm --service-ports node sh -c "npm ci --no-audit --no-fund && npm run dev -- --host 0.0.0.0"

web-test: ## frontend type check and unit tests
	$(DC) run --rm node sh -c "npm ci --no-audit --no-fund && npm run typecheck && npm test"

test: ## run all Go tests (integration tests use the compose PostgreSQL)
	$(GO) go test ./...

test-race: ## run all Go tests under the race detector, 10 times
	$(GO) go test -race -count=10 ./...

test-tc: ## run the Go tests with testcontainers-go (starts its own postgres:16 through the Docker socket)
	$(DC) run --rm go-tc go test ./...

demo-race: ## run the intentionally unsafe bidding demo
	$(TOOLS) demo-race

reconcile: ## verify ledger invariants against the database
	$(TOOLS) reconcile

k6-smoke: ## 50 VUs bid on one auction for 30s against an isolated database, then reconcile
	bash k6/run-smoke.sh
