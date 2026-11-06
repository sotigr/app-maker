package domain

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// SecretRef matches ${secret:<name>} references (ADR 0019).
var SecretRef = regexp.MustCompile(`\$\{secret:([A-Za-z0-9_.-]+)\}`)

// secretInputs are the inputs that may carry secret references, by node:
// http.request substitutes them into header values and the URL after
// evaluation; webhook.call names its signing secret.
var secretInputs = map[string][]string{
	"http.request": {"headers", "url"},
	"webhook.call": {"secret"},
}

// secretRefs returns the secrets written in a node's inputs and reports
// references outside the allowed inputs or to secrets without hosts.
func (c *compiler) secretRefs(ptr, node string, in map[string]any) map[string]bool {
	var out map[string]bool
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		walkStrings(in[k], ptr+"/"+k, func(p, s string) {
			for _, m := range SecretRef.FindAllStringSubmatch(s, -1) {
				allowed := false
				for _, a := range secretInputs[node] {
					allowed = allowed || a == k
				}
				switch {
				case !allowed && node == "http.request":
					c.errf(p, "secrets may only be used in headers and url of http.request")
				case !allowed:
					c.errf(p, "%s cannot use secrets", node)
				case node == "http.request" && len(c.d.SecretHosts[m[1]]) == 0:
					c.errf(p, "secret %s has no allowed hosts: add \"secrets\": {%q: {\"hosts\": [\"api.example.com\"]}} to app.json", m[1], m[1])
				default:
					if out == nil {
						out = map[string]bool{}
					}
					out[m[1]] = true
				}
			}
		})
	}
	return out
}

// codeSecrets checks the secrets a code node declares for ctx.http.
func (c *compiler) codeSecrets(ptr string, kind NodeKind, names []string, cn *CompiledNode) {
	for i, name := range names {
		p := fmt.Sprintf("%s/secrets/%d", ptr, i)
		switch {
		case kind != KindCode:
			c.errf(p, "only code nodes declare secrets; write ${secret:%s} in the inputs instead", name)
		case len(c.d.SecretHosts[name]) == 0:
			c.errf(p, "secret %s has no allowed hosts: add \"secrets\": {%q: {\"hosts\": [\"api.example.com\"]}} to app.json", name, name)
		default:
			if cn.Secrets == nil {
				cn.Secrets = map[string]bool{}
			}
			cn.Secrets[name] = true
		}
	}
}

func walkStrings(v any, ptr string, fn func(ptr, s string)) {
	switch x := v.(type) {
	case string:
		fn(ptr, x)
	case map[string]any:
		for k, e := range x {
			walkStrings(e, ptr+"/"+strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"), fn)
		}
	case []any:
		for _, e := range x {
			walkStrings(e, ptr, fn)
		}
	}
}

// HostAllowed reports whether host matches one of the patterns: an exact
// host or "*.domain".
func HostAllowed(host string, patterns []string) bool {
	host = strings.ToLower(host)
	for _, p := range patterns {
		p = strings.ToLower(p)
		if p == host || (strings.HasPrefix(p, "*.") && strings.HasSuffix(host, p[1:])) {
			return true
		}
	}
	return false
}
