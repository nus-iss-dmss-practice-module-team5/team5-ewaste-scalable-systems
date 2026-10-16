COMPOSE_FILE ?= ../../docker-compose.yml
COMPOSE_ENV_FILE ?= $(CURDIR)/.env

COMPOSE = docker compose -f $(COMPOSE_FILE) --env-file $(COMPOSE_ENV_FILE)

.PHONY: test vet build build-services check test-dependencies run-test docker-build docker-build-services docker-run docker-stop \
	compose-config compose-build compose-up compose-down compose-destroy-test compose-reset-test \
	compose-restart compose-ps compose-logs compose-seed-users dummy_precommit

test:
	go test ./...

dummy_precommit:
	python -m pre_commit run --all-files --show-diff-on-failure

vet:
	go vet ./...

build:
	go build ./cmd/server

build-services:
	go build ./cmd/identity-access ./cmd/batch-workflow ./cmd/claim-logistics ./cmd/custody-verification

check: test vet build

test-dependencies:
	go run ./cmd/server -mode test -check-dependencies

run-test:
	go run ./cmd/server -mode test

docker-build:
	docker build -t ewaste-workflow-api:test .

docker-build-services:
	docker build --build-arg SERVICE=identity-access -t ewaste-identity-access-svc:test .
	docker build --build-arg SERVICE=batch-workflow -t ewaste-batch-workflow-svc:test .
	docker build --build-arg SERVICE=claim-logistics -t ewaste-claim-logistics-svc:test .
	docker build --build-arg SERVICE=custody-verification -t ewaste-custody-verification-svc:test .

docker-run: docker-build
	docker run -d --name ewaste-api-test -p 8080:8080 ewaste-workflow-api:test

docker-stop:
	docker stop ewaste-api-test
	docker rm ewaste-api-test

compose-config:
	$(COMPOSE) config

compose-build:
	$(COMPOSE) build

compose-up: compose-config
	$(COMPOSE) up -d --build --force-recreate

compose-down:
	$(COMPOSE) down

compose-destroy-test:
	@echo "WARNING: destroying the Compose test stack, including MySQL and Redis volumes."
	$(COMPOSE) down --volumes --remove-orphans

compose-reset-test: compose-destroy-test
	$(MAKE) compose-seed-users

compose-restart:
	$(COMPOSE) restart

compose-ps:
	$(COMPOSE) ps

compose-logs:
	$(COMPOSE) logs -f --tail=100

compose-init-test-schema: compose-up
	sed '/^[[:space:]]*--/d' ../../database/test/mysql/001-auth-schema.sql | \
		$(COMPOSE) exec -T mysql sh -c 'mysql -u"$$MYSQL_USER" -p"$$MYSQL_PASSWORD" "$$MYSQL_DATABASE"'

compose-seed-users: compose-init-test-schema
	sed -E '/^--(liquibase|changeset|comment|rollback)/d; s/^INSERT INTO users/INSERT IGNORE INTO users/' ../../database/liquibase/seed/103-seed-users.sql | \
		$(COMPOSE) exec -T mysql sh -c 'mysql -u"$$MYSQL_USER" -p"$$MYSQL_PASSWORD" "$$MYSQL_DATABASE"'
