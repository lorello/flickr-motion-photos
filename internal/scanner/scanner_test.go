package scanner

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"motionphotos/internal/flickrclient"
	"motionphotos/internal/motiondetect"
	"motionphotos/internal/statestore"
)

const testTemplate = `<!doctype html><title>{{.Title}}</title><img src="{{.ImageURL}}"><video src="{{.VideoURL}}"></video><a href="{{.PhotoPageURL}}"></a>`
const testUsername = "testuser"

type fakeReader struct {
	originalBytes []byte
	originalErr   error
	description   string
	displayURL    string
	private       bool // zero value = public, matching the common case in tests
	visibilityErr error
}

func (f *fakeReader) GetPhotosPage(userID string, page, perPage int) ([]flickrclient.Photo, error) {
	return nil, nil
}
func (f *fakeReader) GetPhotoOriginalBytes(photoID string) ([]byte, error) {
	return f.originalBytes, f.originalErr
}
func (f *fakeReader) GetPhotoDisplayURL(photoID string) (string, error) {
	return f.displayURL, nil
}
func (f *fakeReader) GetPhotoDescription(photoID string) (string, error) { return f.description, nil }
func (f *fakeReader) GetPhotoDetails(photoID string) (flickrclient.PhotoDetails, error) {
	return flickrclient.PhotoDetails{}, nil
}
func (f *fakeReader) GetPhotoExif(photoID string) (flickrclient.PhotoExif, error) {
	return flickrclient.PhotoExif{}, nil
}
func (f *fakeReader) GetPhotoFavoritesCount(photoID string) (string, error) { return "", nil }
func (f *fakeReader) GetPhotoGeo(photoID string) (string, string, bool, error) {
	return "", "", false, nil
}
func (f *fakeReader) GetPhotoGroups(photoID string) ([]string, error) { return nil, nil }
func (f *fakeReader) GetPhotoPeople(photoID string) ([]string, error) { return nil, nil }
func (f *fakeReader) IsFullyPrivate(photoID string) (bool, error) {
	return f.private, f.visibilityErr
}

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

func (f *fakePublisher) PublishPage(photoID, html string) (string, error) {
	f.called = true
	return "https://viewer.example.com/p/" + photoID + ".html", nil
}

// withFixedToken makes generateTokenFunc return a fixed value, so tests
// can assert on exact video/page URLs instead of a random token.
func withFixedToken(t *testing.T, token string) {
	t.Helper()
	orig := generateTokenFunc
	generateTokenFunc = func() (string, error) { return token, nil }
	t.Cleanup(func() { generateTokenFunc = orig })
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
	status, _, err := ProcessPhoto(&fakeReader{}, &fakeWriter{}, &fakeUploader{}, &fakePublisher{}, newFakeStore(t), testTemplate, testUsername, photo)
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
	status, _, err := ProcessPhoto(&fakeReader{}, &fakeWriter{}, &fakeUploader{}, &fakePublisher{}, store, testTemplate, testUsername, photo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "skipped(cache)" {
		t.Fatalf("got %s", status)
	}
}

func TestProcessPhotoSkipsNativeFlickrVideoWithoutDownloading(t *testing.T) {
	reader := &fakeReader{originalErr: fmt.Errorf("should never be called for a native video")}
	photo := flickrclient.Photo{ID: "2b", Title: "t", Media: "video"}
	store := newFakeStore(t)

	status, _, err := ProcessPhoto(reader, &fakeWriter{}, &fakeUploader{}, &fakePublisher{}, store, testTemplate, testUsername, photo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "skipped(video)" {
		t.Fatalf("got %s", status)
	}
	if _, ok := store.Get("2b"); ok {
		t.Fatal("expected no cache entry for a native video skip")
	}
}

func TestProcessPhotoBlockedWithoutOAuthDoesNotError(t *testing.T) {
	reader := &fakeReader{originalErr: fmt.Errorf("foto 3 (candownload=0): %w", flickrclient.ErrOriginalNotAvailable)}
	photo := flickrclient.Photo{ID: "3", Title: "t"}
	store := newFakeStore(t)

	status, _, err := ProcessPhoto(reader, &fakeWriter{}, &fakeUploader{}, &fakePublisher{}, store, testTemplate, testUsername, photo)
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

	status, _, err := ProcessPhoto(reader, &fakeWriter{}, &fakeUploader{}, &fakePublisher{}, store, testTemplate, testUsername, photo)
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

	status, _, err := ProcessPhoto(reader, writer, &fakeUploader{}, &fakePublisher{}, store, testTemplate, testUsername, photo)
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
	withFixedToken(t, "tok5")

	reader := &fakeReader{originalBytes: []byte("JPEGDATA"), description: "Descrizione originale", displayURL: "https://live.staticflickr.com/x/5_secret_b.jpg"}
	writer := &fakeWriter{}
	uploader := &fakeUploader{}
	publisher := &fakePublisher{}
	store := newFakeStore(t)
	photo := flickrclient.Photo{ID: "5", Title: "PXL_5.MP"}

	status, viewerURL, err := ProcessPhoto(reader, writer, uploader, publisher, store, testTemplate, testUsername, photo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "published" {
		t.Fatalf("got %s", status)
	}
	if viewerURL != "https://viewer.example.com/p/tok5.html" {
		t.Fatalf("unexpected viewerURL: %s", viewerURL)
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

func TestProcessPhotoMotionButPrivateDefersPublishing(t *testing.T) {
	orig := detectMotionPhotoFunc
	detectMotionPhotoFunc = func([]byte) motiondetect.Result {
		return motiondetect.Result{IsMotion: true, VideoBytes: []byte("VIDEO"), Reason: "published"}
	}
	t.Cleanup(func() { detectMotionPhotoFunc = orig })
	withFixedToken(t, "tok7")

	reader := &fakeReader{originalBytes: []byte("JPEGDATA"), private: true}
	writer := &fakeWriter{}
	uploader := &fakeUploader{}
	publisher := &fakePublisher{}
	store := newFakeStore(t)
	photo := flickrclient.Photo{ID: "7", Title: "PXL_7.MP"}

	status, viewerURL, err := ProcessPhoto(reader, writer, uploader, publisher, store, testTemplate, testUsername, photo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "ready(private)" {
		t.Fatalf("got %s", status)
	}
	if viewerURL != "" {
		t.Fatalf("expected no viewerURL while private, got %s", viewerURL)
	}
	if !uploader.called {
		t.Fatal("expected the video to be extracted and uploaded even while private")
	}
	if publisher.called {
		t.Fatal("expected no page publish while private")
	}
	if len(writer.setDescriptionCalls) != 0 || len(writer.addTagCalls) != 0 {
		t.Fatal("expected no Flickr writes while private")
	}
	r, ok := store.Get("7")
	if !ok || r.Status != "ready-private" || r.VideoURL != "https://videos.example.com/tok7.mp4" || r.Token != "tok7" {
		t.Fatalf("expected ready-private cache entry with VideoURL/Token, got %+v (ok=%v)", r, ok)
	}
}

func TestProcessPhotoReadyPrivateStillPrivateSkipsCheaply(t *testing.T) {
	reader := &fakeReader{private: true}
	uploader := &fakeUploader{}
	publisher := &fakePublisher{}
	store := newFakeStore(t)
	store.Set("8", statestore.Record{Status: "ready-private", VideoURL: "https://videos.example.com/8.mp4"})
	photo := flickrclient.Photo{ID: "8", Title: "PXL_8.MP"}

	status, _, err := ProcessPhoto(reader, &fakeWriter{}, uploader, publisher, store, testTemplate, testUsername, photo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "skipped(still-private)" {
		t.Fatalf("got %s", status)
	}
	if uploader.called {
		t.Fatal("expected no re-download/re-upload on recheck")
	}
	if publisher.called {
		t.Fatal("expected no publish while still private")
	}
}

func TestProcessPhotoReadyPrivateNowPublicFinishesPublishing(t *testing.T) {
	reader := &fakeReader{
		private:     false,
		description: "Original caption",
		displayURL:  "https://live.staticflickr.com/x/9_secret_b.jpg",
	}
	writer := &fakeWriter{}
	uploader := &fakeUploader{}
	publisher := &fakePublisher{}
	store := newFakeStore(t)
	store.Set("9", statestore.Record{Status: "ready-private", VideoURL: "https://videos.example.com/tok9.mp4", Token: "tok9"})
	photo := flickrclient.Photo{ID: "9", Title: "PXL_9.MP"}

	status, viewerURL, err := ProcessPhoto(reader, writer, uploader, publisher, store, testTemplate, testUsername, photo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "published" {
		t.Fatalf("got %s", status)
	}
	if viewerURL != "https://viewer.example.com/p/tok9.html" {
		t.Fatalf("unexpected viewerURL: %s", viewerURL)
	}
	if uploader.called {
		t.Fatal("expected no re-upload, videoURL reused from cache")
	}
	if !publisher.called {
		t.Fatal("expected the page to be published now that the photo is public")
	}
	if len(writer.setDescriptionCalls) != 1 {
		t.Fatalf("expected 1 set_description call, got %d", len(writer.setDescriptionCalls))
	}
	if r, ok := store.Get("9"); !ok || r.Status != "published" {
		t.Fatalf("expected cache entry published, got %+v (ok=%v)", r, ok)
	}
}

func TestProcessPhotoIdempotentSkipsDescriptionIfLinkAlreadyPresent(t *testing.T) {
	orig := detectMotionPhotoFunc
	detectMotionPhotoFunc = func([]byte) motiondetect.Result {
		return motiondetect.Result{IsMotion: true, VideoBytes: []byte("VIDEO"), Reason: "published"}
	}
	t.Cleanup(func() { detectMotionPhotoFunc = orig })
	withFixedToken(t, "tok6")

	reader := &fakeReader{
		originalBytes: []byte("JPEGDATA"),
		description:   "This is a Motion Photo — watch the video: https://viewer.example.com/p/tok6.html",
		displayURL:    "https://live.staticflickr.com/x/6_secret_b.jpg",
	}
	writer := &fakeWriter{}
	store := newFakeStore(t)
	photo := flickrclient.Photo{ID: "6", Title: "PXL_6.MP"}

	status, _, err := ProcessPhoto(reader, writer, &fakeUploader{}, &fakePublisher{}, store, testTemplate, testUsername, photo)
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

func TestProcessPhotoReplacesStaleOwnSentenceInsteadOfDuplicating(t *testing.T) {
	orig := detectMotionPhotoFunc
	detectMotionPhotoFunc = func([]byte) motiondetect.Result {
		return motiondetect.Result{IsMotion: true, VideoBytes: []byte("VIDEO"), Reason: "published"}
	}
	t.Cleanup(func() { detectMotionPhotoFunc = orig })
	withFixedToken(t, "newtoken")

	reader := &fakeReader{
		originalBytes: []byte("JPEGDATA"),
		description:   "Original caption\n\nThis is a Motion Photo — <a href=\"https://viewer.example.com/p/10.html\" rel=\"noreferrer nofollow\">watch the video</a>",
		displayURL:    "https://live.staticflickr.com/x/10_secret_b.jpg",
	}
	writer := &fakeWriter{}
	store := newFakeStore(t)
	photo := flickrclient.Photo{ID: "10", Title: "PXL_10.MP"}

	status, viewerURL, err := ProcessPhoto(reader, writer, &fakeUploader{}, &fakePublisher{}, store, testTemplate, testUsername, photo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "published" {
		t.Fatalf("got %s", status)
	}
	if viewerURL != "https://viewer.example.com/p/newtoken.html" {
		t.Fatalf("unexpected viewerURL: %s", viewerURL)
	}
	if len(writer.setDescriptionCalls) != 1 {
		t.Fatalf("expected 1 set_description call, got %d", len(writer.setDescriptionCalls))
	}
	newDescription := writer.setDescriptionCalls[0]
	if strings.Count(newDescription, "This is a Motion Photo") != 1 {
		t.Fatalf("expected exactly one sentence, got description: %q", newDescription)
	}
	if strings.Contains(newDescription, "p/10.html") {
		t.Fatalf("expected the stale old link to be gone, got description: %q", newDescription)
	}
	if !strings.HasPrefix(newDescription, "Original caption") {
		t.Fatalf("expected the original caption to be preserved, got description: %q", newDescription)
	}
}
