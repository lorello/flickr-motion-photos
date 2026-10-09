package statestore

import (
	"testing"
	"time"
)

func TestSince(t *testing.T) {
	cutoff := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	last := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		m    ScanMeta
		want time.Time
	}{
		{"never scanned: from cutoff", ScanMeta{Cutoff: cutoff}, cutoff},
		{"never scanned, no cutoff: unbounded", ScanMeta{}, time.Time{}},
		{"after full scan: last minus 1y", ScanMeta{Cutoff: cutoff, LastCompleteScan: last}, last.Add(-rescanMargin)},
		{"never before cutoff", ScanMeta{Cutoff: cutoff, LastCompleteScan: cutoff.Add(24 * time.Hour)}, cutoff},
	}
	for _, c := range cases {
		if got := c.m.Since(); !got.Equal(c.want) {
			t.Errorf("%s: Since() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMetaRoundTrip(t *testing.T) {
	dir := t.TempDir()
	m, err := LoadMeta(dir, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if !m.Cutoff.IsZero() || !m.LastCompleteScan.IsZero() {
		t.Fatal("fresh meta must be zero")
	}
	m.Cutoff = time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	m.LastCompleteScan = time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	m2, err := LoadMeta(dir, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if !m2.Cutoff.Equal(m.Cutoff) || !m2.LastCompleteScan.Equal(m.LastCompleteScan) {
		t.Fatalf("round trip mismatch: %+v vs %+v", m2, m)
	}
}
