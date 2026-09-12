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

// GroupInfo, AlbumInfo, PersonInfo and CommentInfo are display-ready
// mirrors of flickrclient's richer types — kept separate so this package
// doesn't need to import flickrclient just to render a struct's fields.
type GroupInfo struct {
	Title   string
	IconURL string
	URL     string
}

type AlbumInfo struct {
	Title     string
	CoverURL  string
	URL       string
	ItemCount int
}

type PersonInfo struct {
	Username   string
	IconURL    string
	ProfileURL string
}

type CommentInfo struct {
	Author     string
	IconURL    string
	ProfileURL string
	Text       string
	Date       string
}

type PageData struct {
	PhotoID          string
	Title            string
	ImageURL         string
	VideoURL         string
	PhotoPageURL     string
	OwnerName        string
	OwnerAvatarURL   string
	OwnerDescription string // the photo owner's own caption, if any — never our own injected sentence (see scanner.StripOwnSentence)
	DateTaken        string
	DateUploaded     string
	Tags             []string
	Views            string
	PageViews        string // Plausible pageviews on this viewer page itself (distinct from Flickr's own Views), optional
	Comments         string // total comment count (for the count badge); the actual list is CommentList
	CommentList      []CommentInfo
	Favorites        string
	LicenseName      string
	LicenseURL       string
	MapEmbedURL      string // pre-built OpenStreetMap embed URL, empty if the photo isn't geotagged (see scanner for how it's built)
	Groups           []GroupInfo
	Albums           []AlbumInfo
	People           []PersonInfo
	Camera           string
	ExposureTime     string
	FNumber          string
	ISO              string
	FocalLength      string
	ContentType      string // "Photo" | "Screenshot" | "Other"
	SafetyLevel      string // "Safe" | "Moderate" | "Restricted"
	ViewPrivacy      string // "Public" | "Friends" | "Family" | "Friends & Family" | "Only you"
	CommentPerm      string // who can comment
	AddMetaPerm      string // who can add tags/notes/people
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
	if data.OwnerAvatarURL != "" && !strings.HasPrefix(data.OwnerAvatarURL, "https://") {
		return "", fmt.Errorf("sitegen: OwnerAvatarURL must be https://, got %q", data.OwnerAvatarURL)
	}
	if data.MapEmbedURL != "" && !strings.HasPrefix(data.MapEmbedURL, "https://") {
		return "", fmt.Errorf("sitegen: MapEmbedURL must be https://, got %q", data.MapEmbedURL)
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
