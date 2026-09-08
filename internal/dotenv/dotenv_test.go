package dotenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSetsUnsetVars(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	os.WriteFile(path, []byte("FOO_TEST_VAR=bar\n# comment\nBAZ_TEST_VAR=\"quoted\"\n\nEMPTY_LINE_ABOVE=1\n"), 0644)

	os.Unsetenv("FOO_TEST_VAR")
	os.Unsetenv("BAZ_TEST_VAR")
	os.Unsetenv("EMPTY_LINE_ABOVE")

	if err := Load(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v := os.Getenv("FOO_TEST_VAR"); v != "bar" {
		t.Fatalf("FOO_TEST_VAR = %q", v)
	}
	if v := os.Getenv("BAZ_TEST_VAR"); v != "quoted" {
		t.Fatalf("BAZ_TEST_VAR = %q", v)
	}
	if v := os.Getenv("EMPTY_LINE_ABOVE"); v != "1" {
		t.Fatalf("EMPTY_LINE_ABOVE = %q", v)
	}
}

func TestLoadDoesNotOverrideExistingEnvVar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	os.WriteFile(path, []byte("REAL_ENV_TEST_VAR=fromfile\n"), 0644)

	os.Setenv("REAL_ENV_TEST_VAR", "fromreal")
	t.Cleanup(func() { os.Unsetenv("REAL_ENV_TEST_VAR") })

	if err := Load(path); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v := os.Getenv("REAL_ENV_TEST_VAR"); v != "fromreal" {
		t.Fatalf("expected real env var to win, got %q", v)
	}
}

func TestLoadMissingFileIsNotError(t *testing.T) {
	if err := Load("/nonexistent/path/.env"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
