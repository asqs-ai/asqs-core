package jobrunner

import "strings"

// Redaction of credential-bearing `docker run -e` values before they reach a log line.
//
// Every eval step and every bootstrap step is logged as a copy-pasteable `docker run …`
// invocation (FormatDockerInvocation, called from DockerRunner.Run), and that argv carries the
// container environment verbatim. ASQS injects at least one env var whose value is a live
// credential: VSS_NUGET_EXTERNAL_FEED_ENDPOINTS is a JSON envelope with a personal access token
// per feed (config.AzureDevOpsNuGetDockerEnv), so any run against Azure Artifacts printed a PAT
// to stderr — and stderr is what operators paste into tickets.
//
// Redaction lives at the formatting boundary rather than at the call site so that every current
// and future caller of FormatDockerInvocation is covered by construction. The cost is that a
// logged invocation for a credentialed run is no longer copy-pasteable as-is; that is the
// intended trade.

// redactedEnvValue replaces a credential value. Deliberately fixed-width and content-free: an
// operator learns that the variable was set, and nothing about what it was set to.
const redactedEnvValue = "***"

// sensitiveEnvExact lists names that carry a secret but contain no credential-shaped word, so
// neither the substring nor the token rule below can catch them.
var sensitiveEnvExact = map[string]bool{
	"VSS_NUGET_EXTERNAL_FEED_ENDPOINTS": true,
}

// sensitiveEnvSubstrings match against the name with separators removed, so API_KEY, APIKEY and
// MY_API_KEY_2 all match "APIKEY". Every entry is a word that is unambiguous wherever it appears.
var sensitiveEnvSubstrings = []string{
	"PASSWORD", "PASSWD", "SECRET", "TOKEN", "CREDENTIAL",
	"APIKEY", "ACCESSKEY", "PRIVATEKEY",
}

// sensitiveEnvTokens match only as a whole `_`/`-`/`.`-delimited token. This rule exists because
// of "PAT": a substring match on it redacts PATH, which is both wrong and actively unhelpful when
// diagnosing a container. "KEY" has the same problem (MONKEY_ID), and "AUTH" a milder one.
var sensitiveEnvTokens = map[string]bool{
	"PAT": true, "PATS": true,
	"AUTH": true, "AUTHORIZATION": true,
	"KEY": true, "KEYS": true,
	"CREDS": true,
}

// redactSecretArgs returns a copy of args with the value of every credential-bearing `-e` /
// `--env` pair masked. The input slice is never mutated: callers pass the same slice they are
// about to hand to exec, and redacting in place would send `***` to the container.
func redactSecretArgs(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i < len(out); i++ {
		switch {
		case out[i] == "-e" || out[i] == "--env":
			// Docker always consumes the next token as the value of -e, even when that token
			// looks like another flag; mirror that rather than second-guessing the shape.
			if i+1 < len(out) {
				out[i+1] = redactEnvPair(out[i+1])
				i++
			}
		case strings.HasPrefix(out[i], "--env="):
			out[i] = "--env=" + redactEnvPair(strings.TrimPrefix(out[i], "--env="))
		}
	}
	return out
}

// redactEnvPair masks the value half of a NAME=VALUE pair when the name or the value looks
// credential-bearing. A bare NAME (no "=") tells Docker to forward the host's value, so there is
// no secret in the argv to hide.
func redactEnvPair(pair string) string {
	eq := strings.Index(pair, "=")
	if eq < 0 {
		return pair
	}
	name, value := pair[:eq], pair[eq+1:]
	if value == "" || !envValueIsSensitive(name, value) {
		return pair
	}
	return name + "=" + redactedEnvValue
}

// envValueIsSensitive applies the three name rules, then falls back to inspecting the value.
func envValueIsSensitive(name, value string) bool {
	up := strings.ToUpper(strings.TrimSpace(name))
	if sensitiveEnvExact[up] {
		return true
	}
	squashed := strings.NewReplacer("_", "", "-", "", ".", "").Replace(up)
	for _, s := range sensitiveEnvSubstrings {
		if strings.Contains(squashed, s) {
			return true
		}
	}
	for _, tok := range strings.FieldsFunc(up, isEnvNameSeparator) {
		if sensitiveEnvTokens[tok] {
			return true
		}
	}
	return valueLooksLikeCredentialEnvelope(value)
}

func isEnvNameSeparator(r rune) bool { return r == '_' || r == '-' || r == '.' }

// valueLooksLikeCredentialEnvelope catches a secret carried under a name the denylist does not
// know. It matches the quoted JSON *key*, not the bare word, so a value that merely mentions a
// password in prose is left readable while a credential envelope is masked. This is the shape
// ASQS already ships and the shape a future integration is most likely to reuse.
func valueLooksLikeCredentialEnvelope(value string) bool {
	low := strings.ToLower(value)
	for _, k := range []string{`"password"`, `"token"`, `"secret"`, `"accesstoken"`, `"apikey"`, `"clientsecret"`} {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}
