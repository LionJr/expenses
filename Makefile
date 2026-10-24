.DEFAULT_GOAL := help

.PHONY: help test test-web vet run

help: ## Показать доступные команды
	@printf 'Использование: make <команда>\n\n'
	@awk 'BEGIN { FS = ":.*## " } /^[a-zA-Z0-9_-]+:.*## / { printf "  %-12s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

test: ## Go-тесты; интеграционные требуют TEST_DATABASE_URL
	go test ./...

test-web: ## JavaScript-тесты в Docker (Node на Mac не нужен)
	docker run --rm --network none -v "$(CURDIR):/work:ro" -w /work node:24-alpine node --test web/app_test.js web/auth_test.js

vet: ## Статическая проверка Go-кода
	go vet ./...

run: ## Собрать и запустить приложение и PostgreSQL (нужен .env)
	docker compose --env-file .env -f deploy/docker-compose.yaml up --build
