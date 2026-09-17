package dotnetproj

import "testing"

// One predicate for "is this an SDK-style project", because the repo had three.
//
// The narrow one (runner/dotnet_entry.go) matched the exact string `Sdk="Microsoft.NET.Sdk"`, so
// every ASP.NET Core project — `Microsoft.NET.Sdk.Web` — failed it. A Web-SDK-only repo therefore
// resolved no workspace and could fail every eval step, while bootstrap, using the family match,
// considered the same repo perfectly ordinary.
func TestIsSDKStyle(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"bare SDK", `<Project Sdk="Microsoft.NET.Sdk"></Project>`, true},
		{"web SDK", `<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`, true},
		{"worker SDK", `<Project Sdk="Microsoft.NET.Sdk.Worker"></Project>`, true},
		{"razor SDK", `<Project Sdk="Microsoft.NET.Sdk.Razor"></Project>`, true},
		{"blazor wasm SDK", `<Project Sdk="Microsoft.NET.Sdk.BlazorWebAssembly"></Project>`, true},
		{"single quotes", `<Project Sdk='Microsoft.NET.Sdk.Web'></Project>`, true},
		{"case insensitive", `<project sdk="microsoft.net.sdk.web"></project>`, true},
		{"versioned SDK reference", `<Project Sdk="Microsoft.NET.Sdk/8.0.100"></Project>`, true},
		{"attributes before Sdk", `<Project ToolsVersion="15.0" Sdk="Microsoft.NET.Sdk"></Project>`, true},
		{"whitespace around =", `<Project Sdk = "Microsoft.NET.Sdk" ></Project>`, true},
		{
			name: "legacy non-SDK project",
			content: `<Project ToolsVersion="15.0" xmlns="http://schemas.microsoft.com/developer/msbuild/2003">
  <Import Project="$(MSBuildToolsPath)\Microsoft.CSharp.targets" />
</Project>`,
			want: false,
		},
		{"third-party SDK", `<Project Sdk="MSBuild.Sdk.Extras"></Project>`, false},
		{"empty", "", false},
		{"not a project file at all", `namespace N { class C {} }`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSDKStyle(tc.content); got != tc.want {
				t.Fatalf("IsSDKStyle(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

// The SDK attribute itself is what tells an API project from a library, and CS23 reads it.
func TestSDKName(t *testing.T) {
	cases := []struct{ content, want string }{
		{`<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`, "Microsoft.NET.Sdk.Web"},
		{`<Project Sdk="Microsoft.NET.Sdk/8.0.100"></Project>`, "Microsoft.NET.Sdk"},
		{`<Project Sdk='Microsoft.NET.Sdk'></Project>`, "Microsoft.NET.Sdk"},
		{`<Project ToolsVersion="15.0"></Project>`, ""},
	}
	for _, tc := range cases {
		if got := SDKName(tc.content); got != tc.want {
			t.Errorf("SDKName(%q) = %q, want %q", tc.content, got, tc.want)
		}
	}
}

func TestIsWebSDK(t *testing.T) {
	for _, in := range []string{
		`<Project Sdk="Microsoft.NET.Sdk.Web"></Project>`,
		`<Project Sdk="microsoft.net.sdk.web"></Project>`,
	} {
		if !IsWebSDK(in) {
			t.Errorf("IsWebSDK(%q) = false, want true", in)
		}
	}
	for _, in := range []string{
		`<Project Sdk="Microsoft.NET.Sdk"></Project>`,
		`<Project Sdk="Microsoft.NET.Sdk.Worker"></Project>`,
		``,
	} {
		if IsWebSDK(in) {
			t.Errorf("IsWebSDK(%q) = true, want false", in)
		}
	}
}
