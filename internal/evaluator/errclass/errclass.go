// Package errclass classifies test/build outputs for evaluator policy (infrastructure vs code bugs).
package errclass

import (
	"strings"
)

// Kind returns a short classifier label when the failure looks like missing or invalid environment
// configuration (DB connection string, DB open, etc.), otherwise "". Prefer false negatives.
// Classification is implemented for csharp, java/kotlin/scala (JDBC ecosystem), and javascript/typescript.
func Kind(lang, testOutput string) string {
	if strings.TrimSpace(testOutput) == "" {
		return ""
	}
	lang = strings.ToLower(strings.TrimSpace(lang))
	out := testOutput
	lower := strings.ToLower(out)

	// English phrases that appear in JDBC / driver logs regardless of test language tag.
	if k := kindSharedInfra(lower); k != "" {
		return k
	}

	switch lang {
	case "csharp", "cs":
		return kindCSharp(out, lower)
	case "java", "kotlin", "scala":
		return kindJVM(lower)
	case "javascript", "typescript", "js", "ts":
		return kindJS(lower)
	default:
		return ""
	}
}

// IsInfrastructureOrEnvironmentTestFailure reports whether Kind would return non-empty.
func IsInfrastructureOrEnvironmentTestFailure(lang, testOutput string) bool {
	return Kind(lang, testOutput) != ""
}

func kindSharedInfra(lower string) string {
	if strings.Contains(lower, "communications link failure") {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "unknown database") {
		return "database_open"
	}
	if strings.Contains(lower, "access denied for user") && strings.Contains(lower, "using password") {
		return "connection_configuration"
	}
	return ""
}

func kindCSharp(out, lower string) string {
	if strings.Contains(out, "SqliteConnectionStringBuilder") ||
		strings.Contains(out, "Microsoft.Data.Sqlite") {
		if strings.Contains(out, "initialization string") ||
			strings.Contains(out, "Format of the initialization string") ||
			strings.Contains(out, "does not conform to specification") {
			return "sqlite_connection_string"
		}
	}
	if strings.Contains(lower, "cannot open database") ||
		strings.Contains(lower, "unable to open the database") {
		return "database_open"
	}
	if strings.Contains(out, "ArgumentException") &&
		(strings.Contains(lower, "connection string") || strings.Contains(lower, "connectionstring")) {
		return "connection_configuration"
	}
	return ""
}

func kindJVM(lower string) string {
	if strings.Contains(lower, "java.sql.sqlnontransientconnectionexception") ||
		strings.Contains(lower, "org.hibernate.exception.jdbcconnectionexception") {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "javax.persistence.persistenceexception") &&
		strings.Contains(lower, "unable to build entitymanagerfactory") {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "could not open jpa entitymanager for transaction") &&
		strings.Contains(lower, "could not obtain connection") {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "could not get jdbc connection") ||
		strings.Contains(lower, "failed to obtain jdbc connection") ||
		strings.Contains(lower, "unable to acquire jdbc connection") {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "connection refused") &&
		(strings.Contains(lower, "jdbc") || strings.Contains(lower, "mysql") ||
			strings.Contains(lower, "postgres") || strings.Contains(lower, "5432") ||
			strings.Contains(lower, "3306") || strings.Contains(lower, "1521")) {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "no suitable driver") && strings.Contains(lower, "jdbc") {
		return "connection_configuration"
	}
	if strings.Contains(lower, "the connection attempt failed") && strings.Contains(lower, "postgresql") {
		return "jdbc_connection"
	}
	return ""
}

func kindJS(lower string) string {
	if strings.Contains(lower, "prismaclientinitializationerror") ||
		strings.Contains(lower, "error code: p1001") ||
		strings.Contains(lower, "can't reach database server") ||
		strings.Contains(lower, "cannot reach database server") {
		return "database_open"
	}
	if strings.Contains(lower, "sequelizeconnectionerror") ||
		strings.Contains(lower, "sequelizehosterror") ||
		strings.Contains(lower, "mongoserverselectionerror") {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "connect econnrefused") &&
		(strings.Contains(lower, "5432") || strings.Contains(lower, "3306") ||
			strings.Contains(lower, "27017") || strings.Contains(lower, "6379") ||
			strings.Contains(lower, "postgres") || strings.Contains(lower, "mysql") ||
			strings.Contains(lower, "mongodb") || strings.Contains(lower, "redis")) {
		return "jdbc_connection"
	}
	if strings.Contains(lower, "password authentication failed") && strings.Contains(lower, "postgres") {
		return "connection_configuration"
	}
	return ""
}
