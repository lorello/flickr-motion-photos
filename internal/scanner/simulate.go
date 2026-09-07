package scanner

import "fmt"

// SimulatingWriter/Uploader/Publisher non toccano mai Flickr/R2/git: loggano
// solo cosa farebbero. Usati finché non c'è un access token OAuth per le
// scritture reali (vedi commento in scanner.go).

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
	ViewerBaseURL string
}

func (p SimulatingPublisher) PublishPage(photoID string) error {
	fmt.Printf("[SIMULATE] write site/p/%s.html + git commit+push -> %s/p/%s.html\n", photoID, p.ViewerBaseURL, photoID)
	return nil
}
