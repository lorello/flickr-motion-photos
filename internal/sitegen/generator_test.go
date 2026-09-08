package sitegen

import (
	"strings"
	"testing"
)

const testTemplate = `<!doctype html>
<title>{{.Title}}</title>
<img src="{{.ImageURL}}">
<video src="{{.VideoURL}}" autoplay muted loop playsinline></video>
<a href="{{.PhotoPageURL}}">View on Flickr</a>
`

func TestRenderPhotoPageSubstitutesAllPlaceholders(t *testing.T) {
	html, err := RenderPhotoPage(testTemplate, PageData{
		PhotoID:      "123",
		Title:        "PXL_20260711.MP",
		ImageURL:     "https://live.staticflickr.com/x/123_secret_b.jpg",
		VideoURL:     "https://videos.example.com/123.mp4",
		PhotoPageURL: "https://www.flickr.com/photos/lorello/123/",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
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

func TestRenderPhotoPageEscapesTitleToPreventXSS(t *testing.T) {
	html, err := RenderPhotoPage(testTemplate, PageData{
		PhotoID:      "123",
		Title:        `</title><script>alert(1)</script>`,
		ImageURL:     "https://live.staticflickr.com/x/123_secret_b.jpg",
		VideoURL:     "https://videos.example.com/123.mp4",
		PhotoPageURL: "https://www.flickr.com/photos/lorello/123/",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(html, "<script>") {
		t.Fatalf("expected title to be escaped, got raw <script> in output: %s", html)
	}
}

func TestRenderPhotoPageRejectsNonHTTPSImageURL(t *testing.T) {
	_, err := RenderPhotoPage(testTemplate, PageData{
		Title:        "t",
		ImageURL:     "javascript:alert(1)",
		VideoURL:     "https://videos.example.com/123.mp4",
		PhotoPageURL: "https://www.flickr.com/photos/lorello/123/",
	})
	if err == nil {
		t.Fatal("expected error for non-https ImageURL")
	}
}

func TestRenderPhotoPageRejectsNonHTTPSVideoURL(t *testing.T) {
	_, err := RenderPhotoPage(testTemplate, PageData{
		Title:        "t",
		ImageURL:     "https://live.staticflickr.com/x/123_secret_b.jpg",
		VideoURL:     "javascript:alert(1)",
		PhotoPageURL: "https://www.flickr.com/photos/lorello/123/",
	})
	if err == nil {
		t.Fatal("expected error for non-https VideoURL")
	}
}
