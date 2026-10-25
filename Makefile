default: fmt lint install generate

GOBIN ?= $(shell go env GOPATH)/bin

build:
	go build -v ./...

install: build
	go install -v ./...

lint:
	golangci-lint run

# Points git at .githooks so the pre-commit hook runs golangci-lint.
hooks:
	git config core.hooksPath .githooks

generate:
	go generate ./...

fmt:
	gofmt -s -w -e .

test:
	go test -v -cover -timeout=120s -parallel=10 ./...

# Acceptance tests create real resources on Ghost. Requires GHOST_TOKEN.
testacc:
	TF_ACC=1 go test -v -cover -timeout 120m ./...

# Writes dev.tfrc so Terraform uses the locally installed provider binary.
# Use with: export TF_CLI_CONFIG_FILE=$(PWD)/dev.tfrc
dev-tfrc:
	@printf 'provider_installation {\n  dev_overrides {\n    "pippinmole/ghost" = "%s"\n  }\n  direct {}\n}\n' "$(GOBIN)" > dev.tfrc
	@echo "Wrote dev.tfrc -> export TF_CLI_CONFIG_FILE=$(PWD)/dev.tfrc"

.PHONY: default build install lint hooks generate fmt test testacc dev-tfrc
