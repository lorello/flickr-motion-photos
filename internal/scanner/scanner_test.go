package scanner

import (
	"errors"
	"fmt"
	"testing"

	"motionphotos/internal/flickrclient"
	"motionphotos/internal/motiondetect"
	"motionphotos/internal/statestore"
)

type fakeReader struct {
	originalBytes []byte
	originalErr   error
	description   string
}

func (f *fakeReader) GetPhotosPage(userID string, page, perPage int) ([]flickrclient.Photo, error) {
	return nil, nil
}
func (f *fakeReader) GetPhotoOriginalBytes(photoID string) ([]byte, error) {
	return f.originalBytes, f.originalErr
}
func (f *fakeReader) GetPhotoDescription(photoID string) (string, error) { return f.description, nil }

type fakeWriter struct {
	setDescriptionCalls []string
	addTagCalls         []string
}

func (f *fakeWriter) SetDescription(photoID, description string) error {
	f.setDescriptionCalls = append(f.setDescriptionCalls, description)
	return nil
}
func (f *fakeWriter) AddTag(photoID, tag string) error {
	f.addTagCalls = append(f.addTagCalls, tag)
	return nil
}

type fakeUploader struct{ called bool }

func (f *fakeUploader) UploadVideo(photoID string, videoBytes []byte) (string, error) {
	f.called = true
	return "https://videos.example.com/" + photoID + ".mp4", nil
}

type fakePublisher struct{ called bool }

func (f *fakePublisher) PublishPage(photoID string) error {
	f.called = true
	return nil
}

func newFakeStore(t *testing.T) statestore.Store {
	t.Helper()
	s, err := statestore.NewFileStore(t.TempDir(), "testuser")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return s
}

func TestProcessPhotoSkipsIfAlreadyTaggedOnFlickr(t *testing.T) {
	photo := flickrclient.Photo{ID: "1", Title: "t", Tags: []string{"flickrmp:status=published"}}
	status, err := ProcessPhoto(&fakeReader{}, &fakeWriter{}, &fakeUploader{}, &fakePublisher{}, newFakeStore(t), "https://viewer.example.com", photo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "skipped(flickr-tag)" {
		t.Fatalf("got %s", status)
	}
}

func TestProcessPhotoSkipsIfAlreadyInCache(t *testing.T) {
	store := newFakeStore(t)
	store.Set("2", statestore.Record{Status: "checked"})
	photo := flickrclient.Photo{ID: "2", Title: "t"}
	status, err := ProcessPhoto(&fakeReader{}, &fakeWriter{}, &fakeUploader{}, &fakePublisher{}, store, "https://viewer.example.com", photo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "skipped(cache)" {
		t.Fatalf("got %s", status)
	}
}

func TestProcessPhotoBlockedWithoutOAuthDoesNotError(t *testing.T) {
	reader := &fakeReader{originalErr: fmt.Errorf("foto 3 (candownload=0): %w", flickrclient.ErrOriginalNotAvailable)}
	photo := flickrclient.Photo{ID: "3", Title: "t"}
	store := newFakeStore(t)

	status, err := ProcessPhoto(reader, &fakeWriter{}, &fakeUploader{}, &fakePublisher{}, store, "https://viewer.example.com", photo)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if status != "blocked-no-oauth" {
		t.Fatalf("got %s", status)
	}
	if _, ok := store.Get("3"); ok {
		t.Fatal("expected no cache entry when blocked, so it retries next run")
	}
}

func TestProcessPhotoDownloadErrorDoesNotError(t *testing.T) {
	reader := &fakeReader{originalErr: errors.New("original download failed: HTTP 502")}
	photo := flickrclient.Photo{ID: "3b", Title: "t"}
	store := newFakeStore(t)

	status, err := ProcessPhoto(reader, &fakeWriter{}, &fakeUploader{}, &fakePublisher{}, store, "https://viewer.example.com", photo)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if status != "download-error" {
		t.Fatalf("got %s", status)
	}
	if _, ok := store.Get("3b"); ok {
		t.Fatal("expected no cache entry on transient error, so it retries next run")
	}
}

func TestProcessPhotoCheckedWhenNotMotion(t *testing.T) {
	orig := detectMotionPhotoFunc
	detectMotionPhotoFunc = func([]byte) motiondetect.Result {
		return motiondetect.Result{IsMotion: false, Reason: "checked"}
	}
	t.Cleanup(func() { detectMotionPhotoFunc = orig })

	reader := &fakeReader{originalBytes: []byte("JPEGDATA")}
	writer := &fakeWriter{}
	store := newFakeStore(t)
	photo := flickrclient.Photo{ID: "4", Title: "t"}

	status, err := ProcessPhoto(reader, writer, &fakeUploader{}, &fakePublisher{}, store, "https://viewer.example.com", photo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "checked" {
		t.Fatalf("got %s", status)
	}
	if len(writer.addTagCalls) != 1 || writer.addTagCalls[0] != "flickrmp:status=checked" {
		t.Fatalf("unexpected add tag calls: %v", writer.addTagCalls)
	}
	r, ok := store.Get("4")
	if !ok || r.Status != "checked" {
		t.Fatalf("expected cache entry checked, got %+v (ok=%v)", r, ok)
	}
}

func TestProcessPhotoPublishedFullFlowSimulated(t *testing.T) {
	orig := detectMotionPhotoFunc
	detectMotionPhotoFunc = func([]byte) motiondetect.Result {
		return motiondetect.Result{IsMotion: true, VideoBytes: []byte("VIDEO"), Reason: "published"}
	}
	t.Cleanup(func() { detectMotionPhotoFunc = orig })

	reader := &fakeReader{originalBytes: []byte("JPEGDATA"), description: "Descrizione originale"}
	writer := &fakeWriter{}
	uploader := &fakeUploader{}
	publisher := &fakePublisher{}
	store := newFakeStore(t)
	photo := flickrclient.Photo{ID: "5", Title: "PXL_5.MP"}

	status, err := ProcessPhoto(reader, writer, uploader, publisher, store, "https://viewer.example.com", photo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "published" {
		t.Fatalf("got %s", status)
	}
	if !uploader.called {
		t.Fatal("expected uploader to be called")
	}
	if !publisher.called {
		t.Fatal("expected publisher to be called")
	}
	if len(writer.setDescriptionCalls) != 1 {
		t.Fatalf("expected 1 set_description call, got %d", len(writer.setDescriptionCalls))
	}
	if len(writer.addTagCalls) != 1 || writer.addTagCalls[0] != "flickrmp:status=published" {
		t.Fatalf("unexpected add tag calls: %v", writer.addTagCalls)
	}
	if r, ok := store.Get("5"); !ok || r.Status != "published" {
		t.Fatalf("expected cache entry published, got %+v (ok=%v)", r, ok)
	}
}

func TestProcessPhotoIdempotentSkipsDescriptionIfLinkAlreadyPresent(t *testing.T) {
	orig := detectMotionPhotoFunc
	detectMotionPhotoFunc = func([]byte) motiondetect.Result {
		return motiondetect.Result{IsMotion: true, VideoBytes: []byte("VIDEO"), Reason: "published"}
	}
	t.Cleanup(func() { detectMotionPhotoFunc = orig })

	reader := &fakeReader{
		originalBytes: []byte("JPEGDATA"),
		description:   "Motion-Photo viewer: questa è una foto motion, guardala su https://viewer.example.com/p/6.html",
	}
	writer := &fakeWriter{}
	store := newFakeStore(t)
	photo := flickrclient.Photo{ID: "6", Title: "PXL_6.MP"}

	status, err := ProcessPhoto(reader, writer, &fakeUploader{}, &fakePublisher{}, store, "https://viewer.example.com", photo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "published" {
		t.Fatalf("got %s", status)
	}
	if len(writer.setDescriptionCalls) != 0 {
		t.Fatal("expected no set_description call, link already present")
	}
}
