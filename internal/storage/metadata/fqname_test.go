package metadata

import "testing"

func TestBareFQName(t *testing.T) {
	cases := map[string]string{
		// C# parameterized forms (B25)
		"Ns.Type#M(int,string)":             "Ns.Type#M",
		"Ns.Type#M()":                       "Ns.Type#M",
		"Ns.Type<T>#Add(T)":                 "Ns.Type#Add",
		"Ns.Type<T,U>#Map(Dictionary<T,U>)": "Ns.Type#Map",
		"Ns.Outer.Inner<T>#M<TM>(List<TM>)": "Ns.Outer.Inner#M",
		"Ns.Type#.ctor(string)":             "Ns.Type#.ctor",
		"Ns.Repo<T>":                        "Ns.Repo",
		"Ns.Type<T>#Field":                  "Ns.Type#Field",
		// Cross-language identity: Java and TS names must pass through untouched.
		"com.acme.PricingEngine#quote":               "com.acme.PricingEngine#quote",
		"com.acme.Order":                             "com.acme.Order",
		"src/app.ts#handler":                         "src/app.ts#handler",
		"API_ROUTE:GET:/api/orders@Ns.C#Get(string)": "API_ROUTE:GET:/api/orders@Ns.C#Get",
		"": "",
	}
	for in, want := range cases {
		if got := BareFQName(in); got != want {
			t.Errorf("BareFQName(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestAssignDupOrdinals returns with the stable-identity bundle (CP13), which brings
// assignDupOrdinals itself.
