// Package sitegen renders the static motion-photo viewer page from a
// template. Rendering is pure (no I/O); publishing the result is the
// caller's job (see internal/r2upload).
package sitegen

import "strings"

type PageData struct {
	PhotoID      string
	Title        string
	ImageURL     string
	VideoURL     string
	PhotoPageURL string
}

func RenderPhotoPage(template string, data PageData) string {
	replacer := strings.NewReplacer(
		"{{TITLE}}", data.Title,
		"{{IMAGE_URL}}", data.ImageURL,
		"{{VIDEO_URL}}", data.VideoURL,
		"{{PHOTO_PAGE_URL}}", data.PhotoPageURL,
	)
	return replacer.Replace(template)
}
