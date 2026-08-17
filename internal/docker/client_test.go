package docker

import (
	"reflect"
	"testing"

	"github.com/moby/moby/api/types/container"
)

func TestStripInheritedImageEnvRefreshesImageDefaults(t *testing.T) {
	containerEnv := []string{
		"APP_VERSION=1.0.0",
		"LOG_LEVEL=info",
		"APP_ENV=production",
		"EMPTY=",
	}
	oldImageEnv := []string{
		"APP_VERSION=1.0.0",
		"LOG_LEVEL=debug",
		"PATH=/usr/local/bin:/usr/bin",
		"EMPTY=",
	}

	got := stripInheritedImageEnv(containerEnv, oldImageEnv)
	want := []string{
		"LOG_LEVEL=info",
		"APP_ENV=production",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stripInheritedImageEnv() = %#v, want %#v", got, want)
	}
}

func TestStripInheritedImageEnvKeepsExplicitOverrides(t *testing.T) {
	containerEnv := []string{
		"APP_VERSION=override",
		"FEATURE_FLAG=true",
	}
	oldImageEnv := []string{
		"APP_VERSION=1.0.0",
		"FEATURE_FLAG=false",
	}

	got := stripInheritedImageEnv(containerEnv, oldImageEnv)
	if !reflect.DeepEqual(got, containerEnv) {
		t.Fatalf("stripInheritedImageEnv() = %#v, want %#v", got, containerEnv)
	}
}

func TestStripInheritedImageEnvPreservesMalformedEntries(t *testing.T) {
	containerEnv := []string{
		"APP_VERSION=1.0.0",
		"MALFORMED",
	}
	oldImageEnv := []string{
		"APP_VERSION=1.0.0",
	}

	got := stripInheritedImageEnv(containerEnv, oldImageEnv)
	want := []string{"MALFORMED"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stripInheritedImageEnv() = %#v, want %#v", got, want)
	}
}

func TestStripInheritedImageLabelsRefreshesImageDefaults(t *testing.T) {
	containerLabels := map[string]string{
		"org.opencontainers.image.version": "1.0.0",
		"com.example.team":                 "payments",
		"dockwarden.update.enable":         "true",
	}
	oldImageLabels := map[string]string{
		"org.opencontainers.image.version": "1.0.0",
		"org.opencontainers.image.source":  "https://example.com/repo",
	}

	got := stripInheritedImageLabels(containerLabels, oldImageLabels)
	want := map[string]string{
		// Inherited verbatim from the old image -> dropped so the new image's
		// version label is applied on recreate.
		"com.example.team":         "payments",
		"dockwarden.update.enable": "true",
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stripInheritedImageLabels() = %#v, want %#v", got, want)
	}
}

func TestStripInheritedImageLabelsKeepsExplicitOverrides(t *testing.T) {
	containerLabels := map[string]string{
		"org.opencontainers.image.version": "custom",
		"feature.flag":                     "on",
	}
	oldImageLabels := map[string]string{
		"org.opencontainers.image.version": "1.0.0",
	}

	got := stripInheritedImageLabels(containerLabels, oldImageLabels)
	if !reflect.DeepEqual(got, containerLabels) {
		t.Fatalf("stripInheritedImageLabels() = %#v, want %#v", got, containerLabels)
	}
}

// The scenario the provenance label exists for: an env entry pinned by an
// earlier recreate carries a value from an image two versions back, so it no
// longer matches the current old image's default and exact-match stripping is
// structurally unable to remove it. The recorded override keys can.
func TestProvenanceDropsStalePinsExactMatchCannot(t *testing.T) {
	containerEnv := []string{
		"APP_VERSION=1.0.0", // pinned by an old recreate; old image now says 2.0.0
		"USER_FLAG=on",      // genuine user override
	}
	oldImageEnv := []string{"APP_VERSION=2.0.0"}

	// Exact-match stripping keeps the stale pin — documents the blind spot.
	if got := stripInheritedImageEnv(containerEnv, oldImageEnv); !reflect.DeepEqual(got, containerEnv) {
		t.Fatalf("exact-match strip = %#v, want unchanged %#v", got, containerEnv)
	}

	overrides, ok := parseEnvOverrides(map[string]string{envOverridesLabel: "USER_FLAG"})
	if !ok {
		t.Fatal("parseEnvOverrides() did not find the label")
	}
	got := filterEnvByOverrides(containerEnv, overrides)
	want := []string{"USER_FLAG=on"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterEnvByOverrides() = %#v, want %#v", got, want)
	}
}

func TestFilterEnvByOverridesKeepsMalformedEntries(t *testing.T) {
	env := []string{"KEEP=1", "DROP=2", "MALFORMED"}
	got := filterEnvByOverrides(env, map[string]struct{}{"KEEP": {}})
	want := []string{"KEEP=1", "MALFORMED"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filterEnvByOverrides() = %#v, want %#v", got, want)
	}
}

// An absent label means "provenance unknown" (fall back to exact-match); a
// present-but-empty label means "zero overrides" (drop everything inherited).
// The two must not collapse into each other.
func TestParseEnvOverridesDistinguishesAbsentFromEmpty(t *testing.T) {
	if _, ok := parseEnvOverrides(map[string]string{}); ok {
		t.Fatal("absent label reported as present")
	}

	overrides, ok := parseEnvOverrides(map[string]string{envOverridesLabel: ""})
	if !ok {
		t.Fatal("empty label reported as absent")
	}
	if len(overrides) != 0 {
		t.Fatalf("empty label parsed to %#v, want empty set", overrides)
	}
}

func TestSetEnvOverridesRoundTrips(t *testing.T) {
	cfg := &container.Config{Env: []string{"B=2", "A=1", "MALFORMED"}}
	setEnvOverrides(cfg)

	if got := cfg.Labels[envOverridesLabel]; got != "A,B" {
		t.Fatalf("label = %q, want %q", got, "A,B")
	}

	overrides, ok := parseEnvOverrides(cfg.Labels)
	if !ok {
		t.Fatal("label written by setEnvOverrides not found by parseEnvOverrides")
	}
	want := map[string]struct{}{"A": {}, "B": {}}
	if !reflect.DeepEqual(overrides, want) {
		t.Fatalf("round-trip = %#v, want %#v", overrides, want)
	}
}
