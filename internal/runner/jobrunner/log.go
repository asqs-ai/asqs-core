package jobrunner

import (
	"strconv"
	"strings"
)

// FormatDockerInvocation returns a shell-like single line for logging (copy-paste debugging).
func FormatDockerInvocation(bin string, args []string) string {
	if bin == "" {
		bin = "docker"
	}
	var b strings.Builder
	b.WriteString(bin)
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(shellQuoteArg(a))
	}
	return b.String()
}

func shellQuoteArg(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\n\"'$`\\") {
		return strconv.Quote(s)
	}
	return s
}
