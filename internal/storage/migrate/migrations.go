package migrate

// The migration lists start EMPTY, deliberately: the mechanism lands first so CP11, CP13, CP20 and
// CP55 have somewhere to put their one-shot work, and upstream's migration bodies are ported as
// core reaches the bundles that need them — not up front, where they would reference columns and
// tables core does not have yet.
//
// Rules for adding one (learned upstream, each the expensive way):
//   - Never reuse or renumber an ID; schema_migrations is keyed by it.
//   - A migration may not assume any DDL from schema.sql has been applied — `asqs-core migrate`
//     connects with a raw pool and never runs InitSchema (deliberate: InitSchema also aligns the
//     embedding column and can truncate). Create what you index.
//   - pgvector defines no l2_norm for the vector type; use sqrt(inner_product(v, v)).
// The guard tests in this package enforce the last two on the source.

// EmbeddingsMigrations are one-shot migrations for the embeddings (pgvector) database.
func EmbeddingsMigrations() []Migration {
	return nil
}

// MetadataMigrations are one-shot migrations for the metadata database.
func MetadataMigrations() []Migration {
	return nil
}
