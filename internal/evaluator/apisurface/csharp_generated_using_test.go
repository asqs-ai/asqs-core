package apisurface

import (
	"strings"
	"testing"
)

// The fix loop's unresolved-using gate judges only the usings a ROUND ADDED, against the state the
// round inherited. Generation has no such baseline — every using in a new file is new — so the same
// rule applied here would reject `using Xunit;` in the first test a repository ever gets.
//
// The bound that survives is narrower and is the case actually on record: a using whose ROOT
// namespace belongs to this repository, naming a sub-namespace the repository does not declare.
// `using Shop.Core.Repositories;` in a repo whose only namespaces are Shop.Core and Shop.Api is a
// namespace the model invented, and no package can supply it because the root is the repo's own.
func TestCSharpUnresolvedRepoUsingReason(t *testing.T) {
	repo := csSourceRepo(t, map[string]string{
		"src/Core/OrderService.cs": "namespace Shop.Core;\npublic class OrderService { }",
		"src/Core/Models/Order.cs": "namespace Shop.Core.Models;\npublic class Order { }",
		"src/Api/Controller.cs":    "namespace Shop.Api;\npublic class Controller { }",
	})
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{
			name:    "a sub-namespace the repository does not declare",
			content: "using Xunit;\nusing Shop.Core.Repositories;\npublic class T { }",
			want:    true,
		},
		{
			name:    "a namespace the repository declares",
			content: "using Xunit;\nusing Shop.Core.Models;\npublic class T { }",
			want:    false,
		},
		{
			name:    "an ancestor of a declared namespace",
			content: "using Shop;\npublic class T { }",
			want:    false,
		},
		{
			name:    "a third-party root is never judged — no repo evidence bears on it",
			content: "using Microsoft.AspNetCore.Mvc.Testing;\nusing FluentAssertions;\npublic class T { }",
			want:    false,
		},
		{
			name:    "a test's own namespace declaration is not a using",
			content: "namespace Shop.Tests;\nusing Xunit;\npublic class T { }",
			want:    false,
		},
		{
			name:    "a using inside a verbatim string is not a directive",
			content: "public class T { const string S = @\"using Shop.Core.Repositories;\"; }",
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CSharpUnresolvedRepoUsingReason(repo, tc.content)
			if (got != "") != tc.want {
				t.Fatalf("reason = %q, want violation = %v", got, tc.want)
			}
			if tc.want && !strings.Contains(got, "Shop.Core.Repositories") {
				t.Errorf("reason = %q, want it to name the namespace", got)
			}
		})
	}
}

// With no repository namespaces there is no root to judge against, and silence is the only honest
// answer — the same rule the fix-loop gate follows.
func TestCSharpUnresolvedRepoUsingReason_silentWithoutEvidence(t *testing.T) {
	if got := CSharpUnresolvedRepoUsingReason(t.TempDir(), "using Shop.Core.Repositories;"); got != "" {
		t.Errorf("reason = %q with no repository namespaces", got)
	}
}
