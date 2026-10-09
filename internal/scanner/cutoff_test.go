package scanner

import (
	"testing"
	"time"

	"motionphotos/internal/statestore"
)

type fakeLister map[string]map[string]statestore.Record

func (f fakeLister) RecordsByStatus(s string) map[string]statestore.Record { return f[s] }

type fakeUploadTimes map[string]time.Time

func (f fakeUploadTimes) PhotoUploadTime(id string) (time.Time, error) { return f[id], nil }

func TestDeriveCutoff_UsesOldestMotionPhoto(t *testing.T) {
	uploaded := time.Date(2020, 8, 10, 0, 0, 0, 0, time.UTC)
	lister := fakeLister{
		"published":     {"50170716778": {}, "52618337193": {}},
		"ready-private": {"49887163393": {}},
		"checked":       {"10000": {}}, // non motion: ignorata
	}
	got, err := DeriveCutoff(lister, fakeUploadTimes{"49887163393": uploaded})
	if err != nil {
		t.Fatal(err)
	}
	if want := uploaded.Add(-cutoffMargin); !got.Equal(want) {
		t.Fatalf("cutoff = %v, want %v", got, want)
	}
}

func TestDeriveCutoff_NoMotionPhotosMeansNoLimit(t *testing.T) {
	got, err := DeriveCutoff(fakeLister{}, fakeUploadTimes{})
	if err != nil || !got.IsZero() {
		t.Fatalf("got %v, %v; want zero, nil", got, err)
	}
}
