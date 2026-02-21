.PHONY: build test build-platforms publish-dry publish bump example-serve example-test example-run

build:
	go build -o dist/agent-reverse-proxy ./cmd/agent-reverse-proxy

test:
	go vet ./...
	go test ./...

build-platforms:
	./scripts/build-platforms.sh

publish-dry: build-platforms
	DRY_RUN=true ./scripts/publish.sh

publish: build-platforms
	DRY_RUN=false ./scripts/publish.sh

bump:
	@if [ -z "$(VERSION)" ]; then \
		echo "Usage: make bump VERSION=x.y.z"; \
		exit 1; \
	fi
	@echo "Bumping version to $(VERSION)..."
	@sed -i 's/"version": "[^"]*"/"version": "$(VERSION)"/' package.json
	@sed -i 's/"@choonkeat\/agent-reverse-proxy-\([^"]*\)": "[^"]*"/"@choonkeat\/agent-reverse-proxy-\1": "$(VERSION)"/' package.json
	@sed -i 's/ProxyVersion = "[^"]*"/ProxyVersion = "$(VERSION)"/' main.go
	@echo "Version bumped to $(VERSION)"

EXAMPLE_PORT ?= 9876
TARGET_URL ?= http://localhost:$(EXAMPLE_PORT)

example-serve:
	go build -o dist/example-server ./cmd/example
	PORT=$(EXAMPLE_PORT) ./dist/example-server

example-test:
	cd cmd/example && npm install --silent
	cd cmd/example && TARGET_URL=$(TARGET_URL) node test.mjs

example-run:
	go build -o dist/example-server ./cmd/example
	cd cmd/example && npm install --silent
	@bash -c '\
		PORT=$(EXAMPLE_PORT) ./dist/example-server & \
		PID=$$!; \
		trap "kill $$PID 2>/dev/null; wait $$PID 2>/dev/null" EXIT; \
		for i in $$(seq 1 30); do curl -sf http://localhost:$(EXAMPLE_PORT)/ >/dev/null && break; sleep 0.2; done; \
		cd cmd/example && TARGET_URL=$(TARGET_URL) node test.mjs'
