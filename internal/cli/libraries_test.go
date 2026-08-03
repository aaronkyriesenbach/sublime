package cli_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aaronkyriesenbach/sublime/internal/cli"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return path
}

func TestLibrariesCommand_PrintsRegisteredLibraries(t *testing.T) {
	path := writeConfig(t, `
libraries:
  - name: movies
    path: /media/movies
    languages: [en, pt-BR]
  - name: tv-anime
    path: /media/tv
    languages: [en]
`)

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"libraries", "--config", path})

	if err := root.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	output := out.String()
	for _, want := range []string{"movies", "/media/movies", "en, pt-BR", "tv-anime", "/media/tv"} {
		if !strings.Contains(output, want) {
			t.Errorf("output missing %q; got:\n%s", want, output)
		}
	}
}

func TestLibrariesCommand_MissingConfig(t *testing.T) {
	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"libraries", "--config", filepath.Join(t.TempDir(), "missing.yaml")})

	if err := root.Execute(); err == nil {
		t.Fatal("expected an error for a missing config file, got nil")
	}
}

func TestLibrariesCommand_MalformedConfig(t *testing.T) {
	path := writeConfig(t, `libraries: [this is not valid`)

	root := cli.NewRootCommand()
	out := &bytes.Buffer{}
	root.SetOut(out)
	root.SetErr(out)
	root.SetArgs([]string{"libraries", "--config", path})

	if err := root.Execute(); err == nil {
		t.Fatal("expected an error for a malformed config file, got nil")
	}
}
