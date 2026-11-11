package config

import (
	"strings"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

// Spec 14.10: the replicated topology refuses single-replica adapters,
// naming the key.
func TestReplicatedRefusesLocalAdapters(t *testing.T) {
	_, err := Load("", envOf(map[string]string{"ORGANON_MANIFEST": "x", "ORGANON_TOPOLOGY": "replicated"}))
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
	c, err := Load("", envOf(map[string]string{"ORGANON_MANIFEST": "m", "ORGANON_ROLES": "api,worker", "ORGANON_HTTP_ADDR": ":1", "ORGANON_DEV": "true"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasRole(RoleAPI) || c.HasRole(RoleGateway) || c.HTTP.Addr != ":1" || !c.Dev {
		t.Fatalf("overrides not applied: %+v", c)
	}
	if _, err := Load("", envOf(map[string]string{"ORGANON_MANIFEST": "m", "ORGANON_ROLES": "api,bogus"})); err == nil || !strings.Contains(err.Error(), "roles") {
		t.Fatalf("unknown role: %v", err)
	}
}

// ADR 0017: the admin console and admin accounts can be switched off.
func TestAdminSwitches(t *testing.T) {
	c, err := Load("", envOf(map[string]string{"ORGANON_MANIFEST": "m",
		"ORGANON_DISABLE_ADMIN_CONSOLE": "true", "ORGANON_DISABLE_ADMIN_ACCOUNTS": "1"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Admin.DisableConsole || !c.Admin.DisableAccounts {
		t.Fatalf("switches not applied: %+v", c.Admin)
	}
	if _, err := Load("", envOf(map[string]string{"ORGANON_MANIFEST": "m", "ORGANON_DISABLE_ADMIN_ACCOUNTS": "yes please"})); err == nil ||
		!strings.Contains(err.Error(), "ORGANON_DISABLE_ADMIN_ACCOUNTS") {
		t.Fatalf("invalid boolean: %v", err)
	}
}

// ADR 0028: the dictation transcriber is configured from the environment;
// its key must be a secret reference.
func TestSpeechConfig(t *testing.T) {
	c, err := Load("", envOf(map[string]string{"ORGANON_MANIFEST": "m", "ORGANON_SPEECH_KIND": "openai-compatible",
		"ORGANON_SPEECH_BASE_URL": "https://api.example.com/v1", "ORGANON_SPEECH_API_KEY": "${secret:stt}", "ORGANON_SPEECH_MODEL": "whisper-large"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Speech.Kind != "openai-compatible" || c.Speech.Model != "whisper-large" || c.Speech.APIKey != "${secret:stt}" {
		t.Fatalf("speech: %+v", c.Speech)
	}
	for env, key := range map[string]string{
		"ORGANON_SPEECH_KIND=cloud":                          "speech.kind",
		"ORGANON_SPEECH_KIND=openai-compatible":              "speech.baseURL",
		"ORGANON_SPEECH_KIND=fake,ORGANON_SPEECH_API_KEY=sk": "speech.apiKey",
	} {
		vars := map[string]string{"ORGANON_MANIFEST": "m"}
		for _, kv := range strings.Split(env, ",") {
			k, v, _ := strings.Cut(kv, "=")
			vars[k] = v
		}
		if _, err := Load("", envOf(vars)); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s: want an error naming %s, got %v", env, key, err)
		}
	}
}
