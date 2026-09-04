package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// OllamaKeepAliveJSON renders general.llm.ollama_keep_alive as the JSON value for Ollama's
// keep_alive request field, or nil when the key is unset so the field is omitted and the server's
// own default applies.
//
// The shape matters. Ollama decodes keep_alive through api.Duration: a JSON NUMBER is seconds, with
// any negative value meaning "never unload", while a JSON STRING goes through time.ParseDuration —
// which rejects "-1" because it carries no unit. So an integer is sent as a number and only a
// unit-bearing value such as "30m" is sent as a string. Anything else fails at load time rather than
// as a 400 from the server on the first completion of the run.
func OllamaKeepAliveJSON(raw string) (json.RawMessage, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return json.RawMessage(strconv.FormatInt(n, 10)), nil
	}
	if _, err := time.ParseDuration(s); err == nil {
		b, err := json.Marshal(s)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(b), nil
	}
	return nil, fmt.Errorf("%q is neither an integer number of seconds (-1 = keep loaded) nor a duration such as \"30m\"", s)
}
