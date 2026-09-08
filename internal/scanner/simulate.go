package scanner

import "fmt"

// SimulatingWriter/Uploader/Publisher never touch Flickr/R2: they just log
// what they would do. SimulatingWriter is used by default for Flickr writes
// (setMeta/addTags) until real writes are explicitly enabled; Simulating
// Uploader/Publisher exist for --dry-run / testing even though the default
// wiring in cmd/scanner uses the real R2-backed ones.

type SimulatingWriter struct{}

func (SimulatingWriter) SetDescription(photoID, description string) error {
	fmt.Printf("[SIMULATE] flickr.photos.setMeta photo_id=%s description=%q\n", photoID, description)
	return nil
}

func (SimulatingWriter) AddTag(photoID, tag string) error {
	fmt.Printf("[SIMULATE] flickr.photos.addTags photo_id=%s tag=%s\n", photoID, tag)
	return nil
}

type SimulatingUploader struct {
	PublicBaseURL string
}

func (u SimulatingUploader) UploadVideo(photoID string, videoBytes []byte) (string, error) {
	url := fmt.Sprintf("%s/%s.mp4", u.PublicBaseURL, photoID)
	fmt.Printf("[SIMULATE] upload video photo_id=%s bytes=%d -> %s\n", photoID, len(videoBytes), url)
	return url, nil
}

type SimulatingPublisher struct {
	PublicBaseURL string
}

func (p SimulatingPublisher) PublishPage(photoID, html string) (string, error) {
	url := fmt.Sprintf("%s/p/%s.html", p.PublicBaseURL, photoID)
	fmt.Printf("[SIMULATE] publish page photo_id=%s bytes=%d -> %s\n", photoID, len(html), url)
	return url, nil
}
