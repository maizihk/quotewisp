GO ?= go

.PHONY: test race vet fuzz build image
test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

fuzz:
	$(GO) test ./internal/httpapi -run='^$$' -fuzz='^FuzzQueryParameters$$' -fuzztime=30s
	$(GO) test ./internal/httpapi -run='^$$' -fuzz='^FuzzUUIDPath$$' -fuzztime=30s
	$(GO) test ./internal/httpapi -run='^$$' -fuzz='^FuzzCategories$$' -fuzztime=30s
	$(GO) test ./internal/httpapi -run='^$$' -fuzz='^FuzzProblem$$' -fuzztime=30s

build:
	CGO_ENABLED=0 $(GO) build -trimpath -o bin/sentence-api ./cmd/api

image:
	docker build --build-arg VERSION=dev --build-arg GIT_COMMIT=unknown --build-arg BUILD_TIME=unknown -t sentence-api:dev .
