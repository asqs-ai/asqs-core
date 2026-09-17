package dotnetproj

import (
	"regexp"
	"strings"
)

// reProjectSdkAttr captures the Sdk attribute of the <Project> element, in either quote style and
// with arbitrary attributes before it.
var reProjectSdkAttr = regexp.MustCompile(`(?i)<Project[^>]*\bSdk\s*=\s*["']([^"']+)["']`)

// SDKName returns the project's SDK identifier without any `/version` suffix
// ("Microsoft.NET.Sdk.Web"), or "" when the file declares none.
func SDKName(projectXML string) string {
	m := reProjectSdkAttr.FindStringSubmatch(projectXML)
	if m == nil {
		return ""
	}
	name := strings.TrimSpace(m[1])
	if i := strings.IndexByte(name, '/'); i > 0 { // Sdk="Microsoft.NET.Sdk/8.0.100"
		name = strings.TrimSpace(name[:i])
	}
	return name
}

// IsSDKStyle reports whether a project file uses the modern SDK format.
//
// It matches the whole Microsoft.NET.Sdk FAMILY, not the bare value. This repo had three different
// answers to the same question, and the narrowest one — an exact match on `Sdk="Microsoft.NET.Sdk"`
// in runner/dotnet_entry.go — excluded every ASP.NET Core project, because those declare
// `Microsoft.NET.Sdk.Web`. A Web-SDK-only repository resolved no eval workspace at all, while
// bootstrap, which used the family match, treated the same tree as perfectly ordinary.
//
// Legacy non-SDK projects (ToolsVersion + a Microsoft.CSharp.targets import) still do not match,
// which is the distinction that actually matters, and neither do third-party SDKs.
func IsSDKStyle(projectXML string) bool {
	return strings.HasPrefix(strings.ToLower(SDKName(projectXML)), "microsoft.net.sdk")
}

// IsWebSDK reports whether the project is an ASP.NET Core web project. It is the signal for "this
// project can host an HTTP surface", which CS18's guidance and CS23's surface detection both read.
func IsWebSDK(projectXML string) bool {
	return strings.EqualFold(SDKName(projectXML), "Microsoft.NET.Sdk.Web")
}
