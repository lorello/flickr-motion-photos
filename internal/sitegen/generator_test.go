package sitegen

import (
	"strings"
	"testing"
)

const testTemplate = `<!doctype html>
<title>{{TITLE}}</title>
<img src="{{IMAGE_URL}}">
<video src="{{VIDEO_URL}}" autoplay muted loop playsinline></video>
<a href="{{PHOTO_PAGE_URL}}">View on Flickr</a>
`

func TestRenderPhotoPageSubstitutesAllPlaceholders(t *testing.T) {
	html := RenderPhotoPage(testTemplate, PageData{
		PhotoID:      "123",
		Title:        "PXL_20260711.MP",
		ImageURL:     "https://live.staticflickr.com/x/123_secret_b.jpg",
		VideoURL:     "https://videos.example.com/123.mp4",
		PhotoPageURL: "https://www.flickr.com/photos/lorello/123/",
	})
	if strings.Contains(html, "{{") {
		t.Fatalf("unresolved placeholder in output: %s", html)
	}
	for _, want := range []string{
		"PXL_20260711.MP",
		"https://live.staticflickr.com/x/123_secret_b.jpg",
		"https://videos.example.com/123.mp4",
		"https://www.flickr.com/photos/lorello/123/",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected output to contain %q", want)
		}
	}
}
