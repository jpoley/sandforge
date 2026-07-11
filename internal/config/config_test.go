package config

import "testing"

func TestIsAllDigits(t *testing.T) {
	cases := map[string]bool{
		"0": true, "1001": true, "00": true,
		"": false, "12a": false, " 12": false, "-1": false, "1.0": false,
	}
	for in, want := range cases {
		if got := isAllDigits(in); got != want {
			t.Errorf("isAllDigits(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDockerSocketOrDefault(t *testing.T) {
	c := &Config{}
	if got := c.DockerSocketOrDefault(); got != "/var/run/docker.sock" {
		t.Errorf("default socket = %q", got)
	}
	c.DockerSocket = "/run/user/1000/docker.sock"
	if got := c.DockerSocketOrDefault(); got != c.DockerSocket {
		t.Errorf("custom socket = %q", got)
	}
}

func TestDefaultsAndDerived(t *testing.T) {
	c := Defaults()
	if c.Project != "sandforge" || c.HTTPPort != 3000 {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if got := c.CloneURL(); got != "http://127.0.0.1:3000" {
		t.Errorf("CloneURL = %q", got)
	}
	if got := c.CILabel(); got != "ubuntu-latest:docker://sandforge/ci:ubuntu-22.04" {
		t.Errorf("CILabel = %q", got)
	}
}

func TestComposeEnvIncludesSocketAndGID(t *testing.T) {
	c := Defaults()
	c.DockerGID = "0"
	c.DockerSocket = "/custom/docker.sock"
	c.StateDir = "/tmp/x"
	env := c.ComposeEnv()
	found := map[string]string{}
	for _, kv := range env {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				found[kv[:i]] = kv[i+1:]
				break
			}
		}
	}
	if found["SANDFORGE_DOCKER_GID"] != "0" {
		t.Errorf("SANDFORGE_DOCKER_GID = %q", found["SANDFORGE_DOCKER_GID"])
	}
	if found["SANDFORGE_DOCKER_SOCKET"] != "/custom/docker.sock" {
		t.Errorf("SANDFORGE_DOCKER_SOCKET = %q", found["SANDFORGE_DOCKER_SOCKET"])
	}
}

func TestRewriteLoopbackHost(t *testing.T) {
	cases := map[string]string{
		// loopback hosts are rewritten so the endpoint is reachable from inside the runner container
		"tcp://localhost:2375": "tcp://host.docker.internal:2375",
		"tcp://127.0.0.1:2375": "tcp://host.docker.internal:2375",
		"tcp://0.0.0.0:2375":   "tcp://host.docker.internal:2375",
		"tcp://[::1]:2375":     "tcp://host.docker.internal:2375",
		// a real remote daemon is left untouched
		"tcp://dockerhost.internal:2376": "tcp://dockerhost.internal:2376",
		"tcp://10.0.0.5:2375":            "tcp://10.0.0.5:2375",
	}
	for in, want := range cases {
		if got := rewriteLoopbackHost(in); got != want {
			t.Errorf("rewriteLoopbackHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestResolveRunnerMode(t *testing.T) {
	// Isolate from the ambient developer environment so the test is deterministic.
	t.Setenv("DOCKER_HOST", "")

	// explicit socket: always socket, no probing of the endpoint
	t.Setenv("DOCKER_HOST", "tcp://somewhere:2375")
	if mode, host, err := ResolveRunnerMode("socket"); err != nil || mode != "socket" || host != "" {
		t.Errorf("explicit socket = (%q,%q,%v)", mode, host, err)
	}

	// explicit tcp with a tcp endpoint → tcp, loopback rewritten
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
	if mode, host, err := ResolveRunnerMode("tcp"); err != nil || mode != "tcp" || host != "tcp://host.docker.internal:2375" {
		t.Errorf("explicit tcp = (%q,%q,%v)", mode, host, err)
	}

	// explicit tcp WITHOUT a tcp endpoint → loud error
	t.Setenv("DOCKER_HOST", "unix:///var/run/docker.sock")
	if _, _, err := ResolveRunnerMode("tcp"); err == nil {
		t.Errorf("explicit tcp with unix endpoint should error")
	}

	// auto + unix endpoint → socket
	t.Setenv("DOCKER_HOST", "unix:///run/user/1000/docker.sock")
	if mode, _, err := ResolveRunnerMode("auto"); err != nil || mode != "socket" {
		t.Errorf("auto+unix = (%q,%v), want socket", mode, err)
	}

	// auto + tcp endpoint → tcp
	t.Setenv("DOCKER_HOST", "tcp://192.168.1.10:2375")
	if mode, host, err := ResolveRunnerMode("auto"); err != nil || mode != "tcp" || host != "tcp://192.168.1.10:2375" {
		t.Errorf("auto+tcp = (%q,%q,%v)", mode, host, err)
	}

	// auto + ssh endpoint → unsupported, loud error
	t.Setenv("DOCKER_HOST", "ssh://user@host")
	if _, _, err := ResolveRunnerMode("auto"); err == nil {
		t.Errorf("auto+ssh should error (unsupported for runner)")
	}

	// invalid explicit mode → error
	t.Setenv("DOCKER_HOST", "")
	if _, _, err := ResolveRunnerMode("bananas"); err == nil {
		t.Errorf("invalid mode should error")
	}
}
