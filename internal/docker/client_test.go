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
