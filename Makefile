# Top-level build entry points. Go is run in docker (no local toolchain needed).

GO_IMAGE        ?= golang:1.26
GO_MODCACHE     ?= agent-teams-gomod
GOJSONSCHEMA    ?= github.com/atombender/go-jsonschema@v0.24.1
GO_CAPITALIZE   ?= ID,URL,SHA,MB,MS,USD,SDK,CLI,CPU,PR

UID_GID := $(shell id -u):$(shell id -g)
GO_RUN   = docker run --rm --user $(UID_GID) \
	-e HOME=/tmp -e GOPATH=/tmp/gopath -e GOMODCACHE=/gomod -e GOCACHE=/gomod/.build-cache -e GOFLAGS=-modcacherw \
	-v $(GO_MODCACHE):/gomod -v $(CURDIR):/src

.PHONY: gen gen-protocol test test-protocol check-protocol go-modcache

## gen: regenerate every generated artefact
gen: gen-protocol

## go-modcache: create the shared Go module cache volume owned by the current user
go-modcache:
	@docker volume inspect $(GO_MODCACHE) >/dev/null 2>&1 || docker volume create $(GO_MODCACHE) >/dev/null
	@docker run --rm -v $(GO_MODCACHE):/gomod $(GO_IMAGE) chown $(UID_GID) /gomod

protocol/node_modules: protocol/package.json protocol/package-lock.json
	cd protocol && npm ci --no-audit --no-fund
	@touch $@

## gen-protocol: schemas -> message.json, bundle, manifest, TS types (gen/ts) and Go types (gen/go)
gen-protocol: protocol/node_modules go-modcache
	cd protocol && node scripts/fmt-schemas.mjs && node scripts/bundle.mjs && node scripts/gen-ts.mjs
	$(GO_RUN) -w /src/protocol $(GO_IMAGE) go run $(GOJSONSCHEMA) -p protocol -t --tags json \
		--capitalization $(GO_CAPITALIZE) -o gen/go/types.gen.go gen/bundle.go.schema.json
	cd protocol && node scripts/gen-go.mjs
	$(GO_RUN) -w /src/protocol/gen/go $(GO_IMAGE) gofmt -w types.gen.go registry.gen.go

test: test-protocol

## test-protocol: schema/example checks, TS type check, Go tests
test-protocol: protocol/node_modules go-modcache
	cd protocol && npm test && npm run check:examples && npm run typecheck
	$(GO_RUN) -w /src/protocol/gen/go $(GO_IMAGE) sh -c 'go vet ./... && go test ./...'

## check-protocol: fail when generated protocol files are out of date
check-protocol: gen-protocol
	git diff --exit-code -- protocol
