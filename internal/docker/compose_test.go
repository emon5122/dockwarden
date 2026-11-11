package docker

import (
	"reflect"
	"testing"
	"time"
)

func composeLabels() map[string]string {
	return map[string]string{
		ComposeProjectLabel:     "wa-call-bridge",
		ComposeServiceLabel:     "bridge",
		ComposeConfigFilesLabel: "/srv/wa-call-bridge/compose.yaml",
		ComposeWorkingDirLabel:  "/srv/wa-call-bridge",
		ComposeOneoffLabel:      "False",
	}
}

func TestComposeProjectOf(t *testing.T) {
	p, ok := ComposeProjectOf(composeLabels())
	if !ok {
		t.Fatal("compose labels not recognised")
	}
	want := ComposeProject{Project: "wa-call-bridge", Service: "bridge", WorkingDir: "/srv/wa-call-bridge",
		ConfigFiles: []string{"/srv/wa-call-bridge/compose.yaml"}}
	if !reflect.DeepEqual(p, want) {
		t.Fatalf("got %+v, want %+v", p, want)
	}

	multi := composeLabels()
	multi[ComposeConfigFilesLabel] = "/srv/hp/compose.yaml, /srv/hp/compose.override.yaml"
	multi[ComposeEnvFileLabel] = "/srv/hp/voice.env"
	p, ok = ComposeProjectOf(multi)
	if !ok || len(p.ConfigFiles) != 2 || p.ConfigFiles[1] != "/srv/hp/compose.override.yaml" || len(p.EnvFiles) != 1 {
		t.Fatalf("multi-file project = %+v ok=%v", p, ok)
	}

	for name, mutate := range map[string]func(map[string]string){
		"no labels":     func(l map[string]string) { clear(l) },
		"no service":    func(l map[string]string) { delete(l, ComposeServiceLabel) },
		"no files":      func(l map[string]string) { delete(l, ComposeConfigFilesLabel) },
		"no workdir":    func(l map[string]string) { delete(l, ComposeWorkingDirLabel) },
		"relative file": func(l map[string]string) { l[ComposeConfigFilesLabel] = "compose.yaml" },
		"relative env":  func(l map[string]string) { l[ComposeEnvFileLabel] = "./.env" },
		"one-off run":   func(l map[string]string) { l[ComposeOneoffLabel] = "True" },
		"opted out":     func(l map[string]string) { l[ComposeRollLabel] = "false" },
	} {
		l := composeLabels()
		mutate(l)
		if _, ok := ComposeProjectOf(l); ok {
			t.Errorf("%s: want not a compose roll", name)
		}
	}
}

func TestComposeGlobalArgs(t *testing.T) {
	p := ComposeProject{Project: "homepbx-voice", Service: "titli-agent", WorkingDir: "/srv/home-pbx",
		ConfigFiles: []string{"/srv/home-pbx/compose.voice.yaml"}, EnvFiles: []string{"/srv/home-pbx/voice.env"}}
	want := []string{"--project-name", "homepbx-voice", "--project-directory", "/srv/home-pbx",
		"--file", "/srv/home-pbx/compose.voice.yaml", "--env-file", "/srv/home-pbx/voice.env"}
	if got := p.GlobalArgs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("GlobalArgs = %v, want %v", got, want)
	}
}

func TestComposeBinds(t *testing.T) {
	p := ComposeProject{WorkingDir: "/srv/app",
		ConfigFiles: []string{"/srv/app/compose.yaml", "/srv/app/sub/extra.yaml", "/etc/shared/base.yaml"},
		EnvFiles:    []string{"/srv/app/.env", "/srv/application/x.env"}}
	want := []string{"/srv/app:/srv/app:ro", "/etc/shared:/etc/shared:ro", "/srv/application:/srv/application:ro"}
	if got := p.Binds(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Binds = %v, want %v", got, want)
	}
}

func TestParseComposeConfig(t *testing.T) {
	raw := []byte(`{"name":"wa-call-bridge","services":{"bridge":{"image":"ghcr.io/nexisltd/wa-gateway:latest",
		"labels":{"dockwarden.enable":"true","dockwarden.stop-timeout":"700"},"stop_grace_period":"11m40s","stop_signal":"SIGINT",
		"environment":{"SECRET":"x"}}}}`)
	svc, err := ParseComposeConfig(raw, "bridge")
	if err != nil {
		t.Fatal(err)
	}
	if svc.Image != "ghcr.io/nexisltd/wa-gateway:latest" || svc.StopGracePeriod != 700*time.Second ||
		svc.Labels["dockwarden.stop-timeout"] != "700" || svc.StopSignal != "SIGINT" {
		t.Fatalf("parsed %+v", svc)
	}
	if _, err := ParseComposeConfig(raw, "other"); err == nil {
		t.Fatal("missing service must be an error")
	}
	if svc, err := ParseComposeConfig([]byte(`{"services":{"a":{"image":"x"}}}`), "a"); err != nil || svc.StopGracePeriod != 0 {
		t.Fatalf("no grace period: %+v %v", svc, err)
	}
	if _, err := ParseComposeConfig([]byte(`{"services":{"a":{"stop_grace_period":"soon"}}}`), "a"); err == nil {
		t.Fatal("bad grace period must be an error")
	}
	if _, err := ParseComposeConfig([]byte(`not json`), "a"); err == nil {
		t.Fatal("bad json must be an error")
	}
}

func TestSameImageRef(t *testing.T) {
	for _, c := range []struct {
		a, b string
		same bool
	}{
		{"nginx", "docker.io/library/nginx:latest", true},
		{"ghcr.io/nexisltd/wa-gateway:latest", "ghcr.io/nexisltd/wa-gateway", true},
		{"ghcr.io/nexisltd/wa-gateway:latest", "ghcr.io/nexisltd/wa-gateway:0.7.1", false},
		{"ghcr.io/nexisltd/wa-gateway:", "ghcr.io/nexisltd/wa-gateway:latest", false},
		{"redis:7", "valkey:7", false},
	} {
		if got := SameImageRef(c.a, c.b); got != c.same {
			t.Errorf("SameImageRef(%q, %q) = %v, want %v", c.a, c.b, got, c.same)
		}
	}
}

func TestComposeUnsetVariable(t *testing.T) {
	if !ComposeUnsetVariable(`time="x" level=warning msg="The \"WCB_TAG\" variable is not set. Defaulting to a blank string."`) {
		t.Fatal("unset-variable warning not detected")
	}
	if ComposeUnsetVariable(`level=warning msg="version is obsolete"`) {
		t.Fatal("unrelated warning flagged")
	}
}
