// Package sitegen renders the static motion-photo viewer page from a
// template. Rendering is pure (no I/O); publishing the result is the
// caller's job (see internal/r2upload).
package sitegen

import (
	"bytes"
	"fmt"
	"html/template"
	"strings"
)

type PageData struct {
	PhotoID      string
	Title        string
	ImageURL     string
	VideoURL     string
	PhotoPageURL string
}

// RenderPhotoPage renders template against data using html/template, which
// context-aware escapes every field (text nodes, attribute values, URL
// attributes) — Flickr photo titles are free text the account owner (or,
// on a public photo, anyone who can see it) controls, so raw string
// substitution here would be a stored-XSS hole on a publicly served page.
// ImageURL/VideoURL are additionally required to be plain https:// URLs,
// on top of html/template's own URL-context sanitization.
func RenderPhotoPage(templateSrc string, data PageData) (string, error) {
	if !strings.HasPrefix(data.ImageURL, "https://") {
		return "", fmt.Errorf("sitegen: ImageURL must be https://, got %q", data.ImageURL)
	}
	if !strings.HasPrefix(data.VideoURL, "https://") {
		return "", fmt.Errorf("sitegen: VideoURL must be https://, got %q", data.VideoURL)
	}

	t, err := template.New("page").Parse(templateSrc)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
