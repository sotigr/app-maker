package config

import (
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// Spec 14.10: the replicated topology refuses single-replica adapters,
// naming the key.
func TestReplicatedRefusesLocalAdapters(t *testing.T) {
	_, err := Load("", envOf(map[string]string{"FW_MANIFEST": "x", "FW_TOPOLOGY": "replicated"}))
	if err == nil {
		t.Fatal("replicated topology accepted memory adapters")
	}
	for _, key := range []string{"repositories.default.driver", "credentials.driver", "jobs.driver", "secrets.cursorKey"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not name %s: %v", key, err)
		}
	}
}

func TestEnvOverrides(t *testing.T) {
	c, err := Load("", envOf(map[string]string{"FW_MANIFEST": "m", "FW_ROLES": "api,worker", "FW_HTTP_ADDR": ":1", "FW_DEV": "true"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasRole(RoleAPI) || c.HasRole(RoleGateway) || c.HTTP.Addr != ":1" || !c.Dev {
		t.Fatalf("overrides not applied: %+v", c)
	}
	if _, err := Load("", envOf(map[string]string{"FW_MANIFEST": "m", "FW_ROLES": "api,bogus"})); err == nil || !strings.Contains(err.Error(), "roles") {
		t.Fatalf("unknown role: %v", err)
	}
}
