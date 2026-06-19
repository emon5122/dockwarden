package docker

import (
	"reflect"
	"testing"
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
