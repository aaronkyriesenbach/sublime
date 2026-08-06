package config_test

import (
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/config"
)

func TestLoadProviderSecrets_FromEnv(t *testing.T) {
	t.Setenv("SUBLIME_OPENSUBTITLES_API_KEY", "test-api-key")
	t.Setenv("SUBLIME_OPENSUBTITLES_USERNAME", "test-user")
	t.Setenv("SUBLIME_OPENSUBTITLES_PASSWORD", "test-pass")

	secrets := config.LoadProviderSecrets()

	if secrets.OpenSubtitles.APIKey != "test-api-key" {
		t.Errorf("APIKey = %q, want %q", secrets.OpenSubtitles.APIKey, "test-api-key")
	}
	if secrets.OpenSubtitles.Username != "test-user" {
		t.Errorf("Username = %q, want %q", secrets.OpenSubtitles.Username, "test-user")
	}
	if secrets.OpenSubtitles.Password != "test-pass" {
		t.Errorf("Password = %q, want %q", secrets.OpenSubtitles.Password, "test-pass")
	}
}

func TestLoadProviderSecrets_Unset(t *testing.T) {
	t.Setenv("SUBLIME_OPENSUBTITLES_API_KEY", "")
	t.Setenv("SUBLIME_OPENSUBTITLES_USERNAME", "")
	t.Setenv("SUBLIME_OPENSUBTITLES_PASSWORD", "")

	secrets := config.LoadProviderSecrets()

	if secrets.OpenSubtitles.APIKey != "" || secrets.OpenSubtitles.Username != "" || secrets.OpenSubtitles.Password != "" {
		t.Errorf("expected empty secrets, got %+v", secrets.OpenSubtitles)
	}
}

func TestLoadProviderSecrets_SubDLFromEnv(t *testing.T) {
	t.Setenv("SUBLIME_SUBDL_API_KEY", "test-subdl-key")

	secrets := config.LoadProviderSecrets()

	if secrets.SubDL.APIKey != "test-subdl-key" {
		t.Errorf("SubDL.APIKey = %q, want %q", secrets.SubDL.APIKey, "test-subdl-key")
	}
}

func TestLoadProviderSecrets_SubDLUnset(t *testing.T) {
	t.Setenv("SUBLIME_SUBDL_API_KEY", "")

	secrets := config.LoadProviderSecrets()

	if secrets.SubDL.APIKey != "" {
		t.Errorf("expected empty SubDL.APIKey, got %q", secrets.SubDL.APIKey)
	}
}
