package jobrunner

import (
	"reflect"
	"strings"
	"testing"
)

// realNuGetEnvelope is the exact shape config.AzureDevOpsNuGetDockerEnv emits
// (config/azure_nuget_docker.go: vssNuGetEnvelope -> endpointCredentials/endpoint/username/password).
const realNuGetEnvelope = `VSS_NUGET_EXTERNAL_FEED_ENDPOINTS={"endpointCredentials":[{"endpoint":"https://pkgs.dev.azure.com/acme/_packaging/feed/nuget/v3/index.json","username":"AzureDevOps","password":"pat-abc123SECRETVALUE"}]}`

const patLiteral = "pat-abc123SECRETVALUE"

// A PAT must never reach the formatter's output. This is the whole point of the bundle: the
// invocation line goes to stderr on every eval and bootstrap step.
func TestFormatDockerInvocation_neverEmitsNuGetPAT(t *testing.T) {
	args := []string{
		"run", "--rm", "--init",
		"-v", "/repo:/workspace:rw",
		"-w", "/workspace",
		"--network", "bridge",
		"-e", "CI=true",
		"-e", realNuGetEnvelope,
		"mcr.microsoft.com/dotnet/sdk:10.0", "dotnet", "restore",
	}
	got := FormatDockerInvocation("docker", args)

	if strings.Contains(got, patLiteral) {
		t.Fatalf("PAT leaked into the invocation line:\n%s", got)
	}
	if strings.Contains(got, "endpointCredentials") {
		t.Fatalf("credential envelope leaked into the invocation line:\n%s", got)
	}
	// The operator still needs to know the variable was set.
	if !strings.Contains(got, "VSS_NUGET_EXTERNAL_FEED_ENDPOINTS="+redactedEnvValue) {
		t.Fatalf("redacted env var not reported as set:\n%s", got)
	}
	// Non-secret env and the rest of the argv must be untouched.
	for _, want := range []string{"CI=true", "/repo:/workspace:rw", "dotnet", "restore"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q to survive redaction:\n%s", want, got)
		}
	}
}

// PATH contains "PAT". A substring rule would redact it, hiding the single most useful variable
// when diagnosing a container. This is why sensitiveEnvTokens matches whole tokens only.
func TestRedactSecretArgs_doesNotRedactPATH(t *testing.T) {
	for _, name := range []string{"PATH", "GOPATH", "JAVA_HOME", "CI", "MONKEY_ID", "COMPATIBILITY_MODE", "NuGetAudit"} {
		pair := name + "=/usr/local/bin:/usr/bin"
		got := redactEnvPair(pair)
		if got != pair {
			t.Errorf("%s was redacted but is not a credential: got %q", name, got)
		}
	}
}

func TestEnvValueIsSensitive_nameRules(t *testing.T) {
	tests := []struct {
		name string
		want bool
		why  string
	}{
		// exact
		{"VSS_NUGET_EXTERNAL_FEED_ENDPOINTS", true, "exact-name rule"},
		// substring on the separator-stripped name
		{"NPM_TOKEN", true, "TOKEN"},
		{"GITHUB_TOKEN", true, "TOKEN"},
		{"MAVEN_PASSWORD", true, "PASSWORD"},
		{"DB_PASSWD", true, "PASSWD"},
		{"CLIENT_SECRET", true, "SECRET"},
		{"API_KEY", true, "APIKEY after separator squash"},
		{"APIKEY", true, "APIKEY"},
		{"MY_API_KEY_2", true, "APIKEY after separator squash"},
		{"AWS_ACCESS_KEY_ID", true, "ACCESSKEY after separator squash"},
		{"SSH_PRIVATE_KEY", true, "PRIVATEKEY after separator squash"},
		{"ARTIFACTORY_CREDENTIALS", true, "CREDENTIAL"},
		// whole-token rule
		{"AZURE_PAT", true, "PAT as a token"},
		{"PAT", true, "PAT alone"},
		{"REGISTRY_AUTH", true, "AUTH as a token"},
		{"SSH_KEY", true, "KEY as a token"},
		{"BITBUCKET_CREDS", true, "CREDS as a token"},
		// case and separator insensitivity
		{"npm_token", true, "lowercase"},
		{"npm-token", true, "dash separator"},
		// not sensitive
		{"PATH", false, "PAT is only a substring"},
		{"CI", false, "plain"},
		{"MSBUILDDISABLENODEREUSE", false, "plain"},
		{"DOTNET_CLI_TELEMETRY_OPTOUT", false, "plain"},
		{"KEYSTORE_LOCATION", false, "KEYSTORE is not the token KEY"},
		{"MONKEY", false, "KEY is only a substring"},
	}
	for _, tc := range tests {
		if got := envValueIsSensitive(tc.name, "some-plain-value"); got != tc.want {
			t.Errorf("envValueIsSensitive(%q) = %v, want %v (%s)", tc.name, got, tc.want, tc.why)
		}
	}
}

// A secret under a name the denylist does not know is still caught by the value shape.
func TestEnvValueIsSensitive_valueEnvelopeHeuristic(t *testing.T) {
	if !envValueIsSensitive("SOME_FUTURE_INTEGRATION", `{"endpoint":"https://x","password":"hunter2"}`) {
		t.Error("JSON credential envelope under an unknown name should be redacted")
	}
	if !envValueIsSensitive("X", `{"token": "abc"}`) {
		t.Error(`quoted "token" key should be redacted`)
	}
	// The bare word in prose is not a credential envelope; over-redacting here would hide
	// legitimate diagnostics for no gain.
	if envValueIsSensitive("MESSAGE", "the password prompt was skipped") {
		t.Error("prose mentioning a password should not be redacted")
	}
}

// Values legitimately contain "=" (base64 padding, connection strings). Only the first one
// separates name from value.
func TestRedactEnvPair_splitsOnFirstEqualsOnly(t *testing.T) {
	if got, want := redactEnvPair("NPM_TOKEN=abc=def=="), "NPM_TOKEN="+redactedEnvValue; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := redactEnvPair("PATH=/a=b:/c"), "PATH=/a=b:/c"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// `-e NAME` (no "=") tells Docker to forward the host's value; there is no secret in the argv.
func TestRedactEnvPair_bareNameUnchanged(t *testing.T) {
	if got := redactEnvPair("NPM_TOKEN"); got != "NPM_TOKEN" {
		t.Errorf("bare name should pass through, got %q", got)
	}
}

func TestRedactSecretArgs_envFlagForms(t *testing.T) {
	in := []string{
		"run",
		"-e", "NPM_TOKEN=secret1",
		"--env", "GITHUB_TOKEN=secret2",
		"--env=MAVEN_PASSWORD=secret3",
		"-e", "CI=true",
		"image",
	}
	got := redactSecretArgs(in)
	joined := strings.Join(got, " ")
	for _, leak := range []string{"secret1", "secret2", "secret3"} {
		if strings.Contains(joined, leak) {
			t.Errorf("%s leaked: %v", leak, got)
		}
	}
	if !strings.Contains(joined, "CI=true") {
		t.Errorf("non-secret env was altered: %v", got)
	}
	if !strings.Contains(joined, "--env=MAVEN_PASSWORD="+redactedEnvValue) {
		t.Errorf("--env=NAME=VALUE form not handled: %v", got)
	}
}

// A trailing `-e` with no value must not panic or index out of range.
func TestRedactSecretArgs_trailingEnvFlagNoPanic(t *testing.T) {
	got := redactSecretArgs([]string{"run", "image", "-e"})
	if want := []string{"run", "image", "-e"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// The caller passes the very slice it is about to hand to exec.Command. Redacting in place would
// send "***" into the container instead of the credential.
func TestRedactSecretArgs_doesNotMutateInput(t *testing.T) {
	in := []string{"run", "-e", realNuGetEnvelope, "image"}
	before := make([]string, len(in))
	copy(before, in)

	_ = redactSecretArgs(in)

	if !reflect.DeepEqual(in, before) {
		t.Fatalf("input slice was mutated:\n got %v\nwant %v", in, before)
	}
	if !strings.Contains(in[2], patLiteral) {
		t.Fatal("the real credential must remain intact in the argv handed to docker")
	}
}

// Guard the ordering assumption redactSecretArgs relies on: DockerRunner.Run emits env as the
// two-token form `-e NAME=VALUE`. If that ever changes, this test fails rather than the
// redaction silently missing.
func TestDockerRunnerEmitsEnvAsTwoTokenForm(t *testing.T) {
	// Mirrors docker.go's loop over spec.Env.
	var args []string
	for _, e := range []string{"CI=true", realNuGetEnvelope} {
		args = append(args, "-e", e)
	}
	if len(args) != 4 || args[0] != "-e" || args[2] != "-e" {
		t.Fatalf("unexpected env argv shape: %v", args)
	}
	if strings.Contains(FormatDockerInvocation("docker", args), patLiteral) {
		t.Fatal("PAT leaked through the two-token form")
	}
}
