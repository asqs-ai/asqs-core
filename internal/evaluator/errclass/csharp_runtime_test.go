package errclass

import "testing"

// The C# arm had three patterns, all sqlite/connection-string shaped — one way a test meets its
// environment, and not the common one. A failure against EF Core, SQL Server, Npgsql or
// Testcontainers carried no class at all.
func TestKind_csharpRuntimeEnvironments(t *testing.T) {
	cases := []struct{ name, output, want string }{
		{"EF Core save", `Microsoft.EntityFrameworkCore.DbUpdateException : An error occurred while saving the entity changes.`, "orm_failure"},
		{"EF Core provider", `System.InvalidOperationException : No database provider has been configured for this DbContext.`, "orm_failure"},
		{"Npgsql", `Npgsql.NpgsqlException : Failed to connect to 127.0.0.1:5432`, "database_connection"},
		{"SQL Server login", `Microsoft.Data.SqlClient.SqlException : Login failed for user 'sa'.`, "database_connection"},
		{"Testcontainers", `DotNet.Testcontainers.Containers.ResourceReaperException : Could not connect to Docker daemon`, "container_unavailable"},
		{"app not listening", `System.Net.Http.HttpRequestException : Connection refused (localhost:5000)`, "app_unreachable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Kind("csharp", tc.output); got != tc.want {
				t.Fatalf("Kind = %q, want %q", got, tc.want)
			}
			if got := Kind("cs", tc.output); got != tc.want {
				t.Errorf(`the "cs" alias returned %q, want %q`, got, tc.want)
			}
			if !IsInfrastructureOrEnvironmentTestFailure("csharp", tc.output) {
				t.Error("not reported as an infrastructure/environment failure")
			}
		})
	}
}

// An ordinary assertion failure must carry no class: labelling one as an environment problem sends
// the fixer looking for infrastructure that is working fine.
func TestKind_csharpAssertionFailureHasNoClass(t *testing.T) {
	for _, out := range []string{
		"Assert.Equal() Failure: Values differ\nExpected: 20\nActual:   18",
		"System.ArgumentOutOfRangeException : quantity must be positive (Parameter 'qty')",
	} {
		if got := Kind("csharp", out); got != "" {
			t.Errorf("Kind(%q) = %q, want no class", out, got)
		}
	}
}

// The pre-existing sqlite classes still fire; the new ones are additions, not replacements.
func TestKind_csharpSqliteClassesUnchanged(t *testing.T) {
	const out = `System.ArgumentException : Format of the initialization string does not conform to specification starting at index 0. (Microsoft.Data.Sqlite.SqliteConnectionStringBuilder)`
	if got := Kind("csharp", out); got != "sqlite_connection_string" {
		t.Fatalf("Kind = %q, want sqlite_connection_string", got)
	}
}
