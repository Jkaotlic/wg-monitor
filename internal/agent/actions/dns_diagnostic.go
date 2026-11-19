package actions

import (
	"encoding/json"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
	"net/url"
	"regexp"
)

var diagnosticURL = regexp.MustCompile(`(?i)https?://[^\s"<>]+`)

// DNS details may include private DoH paths or query parameters. The diagnostic
// keeps the server origin and the actual failure, never its credentials.
func dnsDiagnosticJSON(check wire.Check) (string, error) {
	raw, err := json.Marshal(check)
	if err != nil {
		return "", err
	}
	var data any
	if err = json.Unmarshal(raw, &data); err != nil {
		return "", err
	}
	cleanDNSDiagnostic(data)
	raw, err = json.Marshal(data)
	return string(raw), err
}
func cleanDNSDiagnostic(data any) {
	clean := func(s string) string {
		return diagnosticURL.ReplaceAllStringFunc(s, func(raw string) string {
			u, err := url.Parse(raw)
			if err != nil {
				return "[resolver URL]"
			}
			return u.Scheme + "://" + u.Host + "/…"
		})
	}
	switch v := data.(type) {
	case map[string]any:
		for k, item := range v {
			if s, ok := item.(string); ok {
				v[k] = clean(s)
			} else {
				cleanDNSDiagnostic(item)
			}
		}
	case []any:
		for i, item := range v {
			if s, ok := item.(string); ok {
				v[i] = clean(s)
			} else {
				cleanDNSDiagnostic(item)
			}
		}
	}
}
