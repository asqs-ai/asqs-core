.PHONY: build build-indexers test vet db-up db-down clean

# Build the asqs-core CLI.
build:
	go build -o bin/asqs-core ./cmd/asqs-core

# Build the three external language indexers (needs JDK+Maven, Node, .NET SDK 10).
build-indexers:
	cd tools/java-indexer && mvn -q package
	cd tools/js-ts-indexer && npm ci && npm run build
	cd tools/csharp-indexer && dotnet publish -c Release -o publish

test:
	go test ./...

vet:
	go vet ./...

# Start / stop the local Postgres + pgvector.
db-up:
	docker compose up -d

db-down:
	docker compose down

clean:
	rm -rf bin
