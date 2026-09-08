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
{{if .OwnerName}}<div class="owner"><img src="{{.OwnerAvatarURL}}"><span>{{.OwnerName}}</span></div>{{end}}
{{if .OwnerDescription}}<p class="description">{{.OwnerDescription}}</p>{{end}}
{{if .DateTaken}}<time>{{.DateTaken}}</time>{{end}}
{{if .Comments}}<span class="comments">Comments ({{.Comments}})</span>{{end}}
{{if .Tags}}<div class="tags">{{range .Tags}}<span class="tag">{{.}}</span>{{end}}</div>{{end}}
{{if or .Views .Camera}}<div class="infopanel">
{{if .Views}}<span>{{.Views}}</span>{{end}}
{{if .Camera}}<span>{{.Camera}}</span>{{end}}
{{if .ExposureTime}}<span>{{.ExposureTime}}</span>{{end}}
{{if .FNumber}}<span>{{.FNumber}}</span>{{end}}
{{if .ISO}}<span>{{.ISO}}</span>{{end}}
{{if .FocalLength}}<span>{{.FocalLength}}</span>{{end}}
</div>{{end}}
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

func TestRenderPhotoPageIncludesOwnerDateAndTagsWhenPresent(t *testing.T) {
	html, err := RenderPhotoPage(testTemplate, PageData{
		Title:          "t",
		ImageURL:       "https://live.staticflickr.com/x/123_secret_b.jpg",
		VideoURL:       "https://videos.example.com/123.mp4",
		PhotoPageURL:   "https://www.flickr.com/photos/lorello/123/",
		OwnerName:      "lorello",
		OwnerAvatarURL: "https://farm6.staticflickr.com/5348/buddyicons/65791659@N00.jpg",
		DateTaken:      "2026-07-11 11:15:21",
		Tags:           []string{"vacation", "lake"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"lorello", "buddyicons", "2026-07-11 11:15:21", "vacation", "lake"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected output to contain %q, got: %s", want, html)
		}
	}
}

func TestRenderPhotoPageIncludesOwnerDescriptionAndCommentsWhenPresent(t *testing.T) {
	html, err := RenderPhotoPage(testTemplate, PageData{
		Title:            "t",
		ImageURL:         "https://live.staticflickr.com/x/123_secret_b.jpg",
		VideoURL:         "https://videos.example.com/123.mp4",
		PhotoPageURL:     "https://www.flickr.com/photos/lorello/123/",
		OwnerDescription: "A day at the lake",
		Comments:         "3",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"A day at the lake", "Comments (3)"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected output to contain %q, got: %s", want, html)
		}
	}
}

func TestRenderPhotoPageEscapesOwnerDescription(t *testing.T) {
	html, err := RenderPhotoPage(testTemplate, PageData{
		Title:            "t",
		ImageURL:         "https://live.staticflickr.com/x/123_secret_b.jpg",
		VideoURL:         "https://videos.example.com/123.mp4",
		PhotoPageURL:     "https://www.flickr.com/photos/lorello/123/",
		OwnerDescription: `<script>alert(1)</script>`,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(html, "<script>") {
		t.Fatalf("expected owner description to be escaped, got raw <script> in output: %s", html)
	}
}

func TestRenderPhotoPageIncludesViewsAndExifWhenPresent(t *testing.T) {
	html, err := RenderPhotoPage(testTemplate, PageData{
		Title:        "t",
		ImageURL:     "https://live.staticflickr.com/x/123_secret_b.jpg",
		VideoURL:     "https://videos.example.com/123.mp4",
		PhotoPageURL: "https://www.flickr.com/photos/lorello/123/",
		Views:        "1033",
		Camera:       "Google Pixel Fold",
		ExposureTime: "1/1248",
		FNumber:      "f/1.7",
		ISO:          "ISO 44",
		FocalLength:  "4.5 mm",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"1033", "Google Pixel Fold", "1/1248", "f/1.7", "ISO 44", "4.5 mm"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected output to contain %q, got: %s", want, html)
		}
	}
}

func TestRenderPhotoPageOmitsInfopanelWhenAbsent(t *testing.T) {
	html, err := RenderPhotoPage(testTemplate, PageData{
		Title:        "t",
		ImageURL:     "https://live.staticflickr.com/x/123_secret_b.jpg",
		VideoURL:     "https://videos.example.com/123.mp4",
		PhotoPageURL: "https://www.flickr.com/photos/lorello/123/",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(html, "infopanel") {
		t.Fatalf("expected no infopanel when Views/Camera absent, got: %s", html)
	}
}

func TestRenderPhotoPageOmitsOwnerDateTagsWhenAbsent(t *testing.T) {
	html, err := RenderPhotoPage(testTemplate, PageData{
		Title:        "t",
		ImageURL:     "https://live.staticflickr.com/x/123_secret_b.jpg",
		VideoURL:     "https://videos.example.com/123.mp4",
		PhotoPageURL: "https://www.flickr.com/photos/lorello/123/",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, absent := range []string{"owner", "<time>", "tags"} {
		if strings.Contains(html, absent) {
			t.Fatalf("expected no %q in output when field is empty, got: %s", absent, html)
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

func TestRenderPhotoPageRejectsNonHTTPSOwnerAvatarURL(t *testing.T) {
	_, err := RenderPhotoPage(testTemplate, PageData{
		Title:          "t",
		ImageURL:       "https://live.staticflickr.com/x/123_secret_b.jpg",
		VideoURL:       "https://videos.example.com/123.mp4",
		PhotoPageURL:   "https://www.flickr.com/photos/lorello/123/",
		OwnerName:      "lorello",
		OwnerAvatarURL: "javascript:alert(1)",
	})
	if err == nil {
		t.Fatal("expected error for non-https OwnerAvatarURL")
	}
}
