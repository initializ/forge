package cmd

import "testing"

func TestResolveChildUpstream(t *testing.T) {
	const listen = "127.0.0.1:8787"
	gateway := "https://kong.example.net/bedrock/claude-oidc-multi"

	cases := []struct {
		name                          string
		flag, env, settingsBase, want string
	}{
		{"flag wins over everything", gateway, "https://env.example", "https://settings.example", gateway},
		{"env when no flag", "", gateway, "https://settings.example", gateway},
		{"settings gateway when no flag/env (chaining)", "", "", gateway, gateway},
		{"nothing configured → child default", "", "", "", ""},
		{"settings pointing at ourselves is ignored (no self-loop)", "", "", "http://" + listen, ""},
		{"settings pointing at ourselves with scheme+path ignored", "", "", "http://" + listen + "/bedrock", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveChildUpstream(c.flag, c.env, c.settingsBase, listen); got != c.want {
				t.Errorf("resolveChildUpstream(%q,%q,%q,%q) = %q, want %q",
					c.flag, c.env, c.settingsBase, listen, got, c.want)
			}
		})
	}
}
