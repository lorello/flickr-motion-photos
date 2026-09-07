package statestore

import (
	"testing"
)

func TestSetAndGetRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir, "65791659@N00")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := store.Get("123"); ok {
		t.Fatal("expected no record for unseen photo")
	}

	if err := store.Set("123", Record{Status: "published"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r, ok := store.Get("123")
	if !ok {
		t.Fatal("expected record to be present after Set")
	}
	if r.Status != "published" {
		t.Fatalf("got status %q", r.Status)
	}
}

func TestPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir, "65791659@N00")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := store.Set("999", Record{Status: "checked"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	reopened, err := NewFileStore(dir, "65791659@N00")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	r, ok := reopened.Get("999")
	if !ok {
		t.Fatal("expected record to survive reopen")
	}
	if r.Status != "checked" {
		t.Fatalf("got status %q", r.Status)
	}
}

func TestDifferentUserIDsUseDifferentFiles(t *testing.T) {
	dir := t.TempDir()
	storeA, _ := NewFileStore(dir, "userA")
	storeB, _ := NewFileStore(dir, "userB")

	storeA.Set("1", Record{Status: "published"})

	if _, ok := storeB.Get("1"); ok {
		t.Fatal("expected user B cache to be isolated from user A")
	}
	if storeA.Path() == storeB.Path() {
		t.Fatalf("expected different cache file paths, got same: %s", storeA.Path())
	}
}
