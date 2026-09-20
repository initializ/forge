package cmd

import "testing"

// TestPickUpstream covers the pure precedence: managed > flag > env > user > chaining.
func TestPickUpstream(t *testing.T) {
	cases := []struct {
		name                                  string
		managed, flag, env, userCfg, chaining string
		want                                  string
	}{
		{"managed wins over everything (enforced)", "https://managed", "https://flag", "https://env", "https://user", "https://chain", "https://managed"},
		{"flag when no managed", "", "https://flag", "https://env", "https://user", "https://chain", "https://flag"},
		{"env when no flag/managed", "", "", "https://env", "https://user", "https://chain", "https://env"},
		{"forge.yaml user config when no flag/env/managed", "", "", "", "https://user", "https://chain", "https://user"},
		{"chaining last", "", "", "", "", "https://chain", "https://chain"},
		{"nothing → empty", "", "", "", "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickUpstream(c.managed, c.flag, c.env, c.userCfg, c.chaining); got != c.want {
				t.Errorf("pickUpstream(%q,%q,%q,%q,%q) = %q, want %q",
					c.managed, c.flag, c.env, c.userCfg, c.chaining, got, c.want)
			}
		})
	}
}

// TestResolveChildUpstream_SelfLoopGuard checks the daemon's chaining fallback
// drops a Claude Code settings gateway that points back at our own listen addr.
func TestResolveChildUpstream_SelfLoopGuard(t *testing.T) {
	const listen = "127.0.0.1:8787"
	// Isolate from ambient config: clear the env tiers and point HOME at an empty
	// temp dir so ~/.forge/forge.yaml can't leak in.
	t.Setenv(forgeManagedUpstreamEnv, "")
	t.Setenv("FORGE_OPTIMIZER_UPSTREAM", "")
	t.Setenv("HOME", t.TempDir())

	// A real gateway in settings → used as chaining.
	if got := resolveChildUpstream("", "https://kong.example/bedrock", listen); got != "https://kong.example/bedrock" {
		t.Errorf("gateway chaining = %q, want the gateway", got)
	}
	// Settings pointing back at us → dropped, nothing else configured → "".
	if got := resolveChildUpstream("", "http://"+listen, listen); got != "" {
		t.Errorf("self-pointing settings should be ignored, got %q", got)
	}
	// Explicit flag always wins over chaining.
	if got := resolveChildUpstream("https://flag", "http://"+listen, listen); got != "https://flag" {
		t.Errorf("flag should win, got %q", got)
	}
}
