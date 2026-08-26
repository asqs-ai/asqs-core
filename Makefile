.PHONY: build build-indexers test test-live vet db-up db-down clean

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

# Run the packages that carry *_live_test.go files against a real Postgres.
# Point ASQS_TEST_METADATA_URL at a SCRATCH database — the name must contain "test" or "scratch",
# or the tests refuse to write (see internal/storage/metadata/livetest_guard.go). With the
# variable unset every live test skips and this target exits 0.
#
#   docker compose up -d
#   docker compose exec postgres createdb -U asqs asqs_scratch   # once
#   ASQS_TEST_METADATA_URL='postgres://asqs:asqs@localhost:5432/asqs_scratch?sslmode=disable' make test-live
test-live:
	go test -count=1 ./internal/storage/... ./internal/intelligence/...

vet:
	go vet ./...

# Start / stop the local Postgres + pgvector.
db-up:
	docker compose up -d

db-down:
	docker compose down

clean:
	rm -rf bin
