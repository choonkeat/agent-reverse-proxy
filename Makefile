.PHONY: build test build-platforms publish-dry publish bump

build:
	go build -o dist/agent-reverse-proxy .

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
	@sed -i 's/Version: "[^"]*"/Version: "$(VERSION)"/' main.go
	@echo "Version bumped to $(VERSION)"
