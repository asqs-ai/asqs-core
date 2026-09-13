package indexer

import "testing"

// The link between a client call and the route that serves it is an exact string compare, and the
// emitters do not agree on the spelling. The C# indexer lower-cases the path and collapses
// parameter segments (ASP.NET routing is case-insensitive, so comparing as written never matches);
// the JS/TS and Java sides do neither. LinkAPIClientRequestsToRoutes runs across the whole
// repository with no language partition, so in a mono-repo — the mixed C#+TS shape the E2E surface
// detection now routes to — a TS client never linked to the C# controller serving it, and the route
// stayed reported as uncovered.
//
// Settling it here rather than in one emitter is the point: the key is the single place both sides
// meet.
func TestRouteMatchKey_agreesAcrossEmitterSpellings(t *testing.T) {
	same := [][2][2]string{
		// C# route (lower-cased, {id} collapsed) vs a TS client literal as written.
		{{"GET", "/api/orders/*"}, {"get", "/api/Orders/{id}"}},
		// Nest's `:id` and ASP.NET's `{id}` are the same parameter.
		{{"GET", "/users/:id"}, {"GET", "/users/{id}"}},
		// A route constraint is still a parameter.
		{{"POST", "/api/orders/{id:int}"}, {"POST", "/api/orders/*"}},
		// A catch-all, and a Spring path variable.
		{{"GET", "/files/{*path}"}, {"GET", "/files/{p}"}},
		// Trailing slash and casing.
		{{"GET", "/API/Orders/"}, {"GET", "/api/orders"}},
	}
	for _, pair := range same {
		a := routeMatchKey(pair[0][0], pair[0][1])
		b := routeMatchKey(pair[1][0], pair[1][1])
		if a != b {
			t.Errorf("routeMatchKey(%q,%q)=%q != routeMatchKey(%q,%q)=%q",
				pair[0][0], pair[0][1], a, pair[1][0], pair[1][1], b)
		}
	}

	// Normalising must not make unrelated paths equal: a parameter matches a parameter, never a
	// literal segment, and a different method is a different route.
	differ := [][2][2]string{
		{{"GET", "/api/orders/{id}"}, {"GET", "/api/orders/all"}},
		{{"GET", "/api/orders"}, {"POST", "/api/orders"}},
		{{"GET", "/api/orders"}, {"GET", "/api/invoices"}},
		{{"GET", "/api/orders/{id}"}, {"GET", "/api/orders/{id}/lines"}},
	}
	for _, pair := range differ {
		a := routeMatchKey(pair[0][0], pair[0][1])
		b := routeMatchKey(pair[1][0], pair[1][1])
		if a == b {
			t.Errorf("routeMatchKey conflated %q %q with %q %q (both %q)",
				pair[0][0], pair[0][1], pair[1][0], pair[1][1], a)
		}
	}
}
