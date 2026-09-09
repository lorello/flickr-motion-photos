// Package scanner orchestra scansione, rilevamento e pubblicazione delle
// motion photo. Le dipendenze a effetto (lettura Flickr, scrittura Flickr,
// upload video, pubblicazione pagina) sono isolate dietro interfacce
// piccole apposta: Uploader/Publisher sono backed da R2 (internal/r2upload),
// Writer resta un'implementazione "Simulating" che logga invece di
// scrivere davvero su Flickr, finché non si decide di attivare le
// scritture reali — si sostituisce solo nel main, ProcessPhoto/Run non
// cambiano.
package scanner

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"

	"motionphotos/internal/flickrclient"
	"motionphotos/internal/motiondetect"
	"motionphotos/internal/plausible"
	"motionphotos/internal/sitegen"
	"motionphotos/internal/statestore"
)

// mapEmbedDelta sets how much area (in degrees) the OpenStreetMap embed
// shows around the photo's coordinates.
const mapEmbedDelta = 0.005

// BuildMapEmbedURL builds an OpenStreetMap embed URL (no API key needed,
// unlike Google Maps) centered on lat/lon, with a marker. Returns an error
// if lat/lon aren't valid floats — callers should treat that as "skip the
// map", not fail the whole publish.
func BuildMapEmbedURL(lat, lon string) (string, error) {
	latF, err := strconv.ParseFloat(lat, 64)
	if err != nil {
		return "", err
	}
	lonF, err := strconv.ParseFloat(lon, 64)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"https://www.openstreetmap.org/export/embed.html?bbox=%f,%f,%f,%f&marker=%f,%f",
		lonF-mapEmbedDelta, latF-mapEmbedDelta, lonF+mapEmbedDelta, latF+mapEmbedDelta, latF, lonF,
	), nil
}

const statusTagPrefix = "flickrmp:status="

// motionPhotoSentencePrefix is the fixed lead-in of the sentence ProcessPhoto
// appends to a photo's description. StripOwnSentence uses it to recover the
// owner's original caption (if any) for display on the viewer page — showing
// our own "watch the video" sentence back on the very page that IS the video
// would be circular.
const motionPhotoSentencePrefix = "This is a Motion Photo — "

// StripOwnSentence returns desc with our own appended sentence (and the
// blank line before it) removed, leaving only the photo owner's original
// caption. Safe to call on a description that never had our sentence.
func StripOwnSentence(desc string) string {
	idx := strings.Index(desc, motionPhotoSentencePrefix)
	if idx < 0 {
		return desc
	}
	return strings.TrimSpace(desc[:idx])
}

// Reader è il sottoinsieme di lettura di flickrclient.Client richiesto dallo
// scanner — separato da Writer così le due responsabilità si possono
// sostituire indipendentemente (es. Reader reale + Writer simulato, come
// in questa fase).
type Reader interface {
	GetPhotosPage(userID string, page, perPage int) ([]flickrclient.Photo, error)
	GetPhotoOriginalBytes(photoID string) ([]byte, error)
	GetPhotoDisplayURL(photoID string) (string, error)
	GetPhotoDescription(photoID string) (string, error)
	GetPhotoDetails(photoID string) (flickrclient.PhotoDetails, error)
	GetPhotoExif(photoID string) (flickrclient.PhotoExif, error)
	GetPhotoFavoritesCount(photoID string) (string, error)
	GetPhotoGeo(photoID string) (lat, lon string, hasGeo bool, err error)
	GetPhotoGroups(photoID string) ([]string, error)
	GetPhotoPeople(photoID string) ([]string, error)
	IsFullyPrivate(photoID string) (bool, error)
}

// Writer scrive metadati su Flickr (setMeta/addTags). L'implementazione
// simulata non chiama mai Flickr: logga solo cosa farebbe.
type Writer interface {
	SetDescription(photoID, description string) error
	AddTag(photoID, tag string) error
}

// Uploader carica il video estratto (su R2) e ritorna l'URL pubblico.
type Uploader interface {
	UploadVideo(photoID string, videoBytes []byte) (string, error)
}

// Publisher pubblica la pagina viewer statica (su R2, stesso bucket dei
// video) e ritorna l'URL pubblico della pagina.
type Publisher interface {
	PublishPage(photoID, html string) (string, error)
}

// detectMotionPhotoFunc è iniettabile nei test.
var detectMotionPhotoFunc = motiondetect.DetectMotionPhoto

// getPageViewsFunc è nil finché EnablePlausible non viene chiamata (feature
// opzionale: senza chiave configurata, PageViews resta vuoto e la riga
// stats semplicemente non la mostra).
var getPageViewsFunc func(path string) (visitors, pageviews int, err error)

// EnablePlausible collega le letture di pageview da Plausible per la riga
// stats della pagina (distinte dal contatore "Views" di Flickr, che conta
// le visite alla FOTO su Flickr, non a questa pagina). Opzionale — se non
// chiamata, il campo PageViews resta sempre vuoto.
func EnablePlausible(apiKey, siteID string) {
	getPageViewsFunc = func(path string) (int, int, error) {
		return plausible.GetPageViews(apiKey, siteID, path)
	}
}

// generateToken produces a random 128-bit hex string used as the public R2
// key for a photo's video/page — unlike the Flickr photo ID (sequential,
// same one printed in the public flickr.com photo URL), this can't be
// guessed or enumerated by timing correlation.
func generateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// generateTokenFunc è iniettabile nei test per rendere deterministico il
// token usato (altrimenti casuale ad ogni chiamata).
var generateTokenFunc = generateToken

func alreadyTagged(tags []string) bool {
	for _, t := range tags {
		if strings.HasPrefix(t, statusTagPrefix) {
			return true
		}
	}
	return false
}

// ProcessPhoto valuta una singola foto. Ritorna uno status descrittivo:
// "skipped" (già nota, da Flickr tag o da cache locale), "checked"/"lost"/
// "published" (esito del rilevamento — include public, friends e family:
// vedi IsFullyPrivate), "blocked-no-oauth" quando l'originale non è
// scaricabile senza un access token OAuth, "download-error" per un
// fallimento di rete/CDN transitorio (es. 502 da live.staticflickr.com),
// "ready(private)" quando è motion ma la foto è ancora completamente
// privata (video estratto e già su R2, pagina NON pubblicata), o
// "skipped(still-private)" per un "ready(private)" ricontrollato che lo è
// ancora. Nessuno dei due casi di errore transitorio è fatale né viene
// salvato in cache: si ritenta al prossimo run. viewerURL è non vuoto solo
// quando status è "published" — utile per il logging del chiamante.
func ProcessPhoto(reader Reader, writer Writer, uploader Uploader, publisher Publisher, store statestore.Store, template, username string, photo flickrclient.Photo) (status string, viewerURL string, err error) {
	if alreadyTagged(photo.Tags) {
		return "skipped(flickr-tag)", "", nil
	}

	if rec, ok := store.Get(photo.ID); ok {
		if rec.Status != "ready-private" {
			return "skipped(cache)", "", nil
		}
		fullyPrivate, err := reader.IsFullyPrivate(photo.ID)
		if err != nil {
			return "", "", err
		}
		if fullyPrivate {
			return "skipped(still-private)", "", nil
		}
		return finishPublish(reader, writer, publisher, store, template, username, photo, rec.VideoURL, rec.Token)
	}

	jpegBytes, err := reader.GetPhotoOriginalBytes(photo.ID)
	if err != nil {
		if errors.Is(err, flickrclient.ErrOriginalNotAvailable) {
			return "blocked-no-oauth", "", nil
		}
		return "download-error", "", nil //nolint:nilerr // transitorio (rete/CDN): si ritenta al prossimo run, non è un errore dello scanner
	}

	result := detectMotionPhotoFunc(jpegBytes)

	if !result.IsMotion {
		if err := store.Set(photo.ID, statestore.Record{Status: result.Reason}); err != nil {
			return "", "", err
		}
		if err := writer.AddTag(photo.ID, statusTagPrefix+result.Reason); err != nil {
			return "", "", err
		}
		return result.Reason, "", nil
	}

	// Token, non l'ID Flickr (sequenziale e prevedibile), è la chiave
	// pubblica del video/pagina — vedi generateToken.
	token, err := generateTokenFunc()
	if err != nil {
		return "", "", err
	}

	videoURL, err := uploader.UploadVideo(token, result.VideoBytes)
	if err != nil {
		return "", "", err
	}

	fullyPrivate, err := reader.IsFullyPrivate(photo.ID)
	if err != nil {
		return "", "", err
	}
	if fullyPrivate {
		// Video already extracted and uploaded — cheap to finish publishing
		// later (IsFullyPrivate check only, no re-download) once the owner
		// makes the photo at least friends/family visible. Not tagged:
		// alreadyTagged must stay false so this photo keeps getting the
		// cheap recheck each run.
		if err := store.Set(photo.ID, statestore.Record{Status: "ready-private", VideoURL: videoURL, Token: token}); err != nil {
			return "", "", err
		}
		return "ready(private)", "", nil
	}

	return finishPublish(reader, writer, publisher, store, template, username, photo, videoURL, token)
}

// finishPublish renders and publishes the viewer page for a motion photo
// whose video is already on R2 (videoURL, at key token), then writes the
// description link and status tag. Shared by ProcessPhoto's direct-publish
// path and its "was private, now at least friends/family" resume path.
// Publishing at this point is safe for a friends/family-only photo too:
// whoever can already see its Flickr description (Flickr's own access
// control) gets the link, same trust boundary Flickr already drew — the
// token keeps the URL from being guessed by anyone else.
func finishPublish(reader Reader, writer Writer, publisher Publisher, store statestore.Store, template, username string, photo flickrclient.Photo, videoURL, token string) (string, string, error) {
	imageURL, err := reader.GetPhotoDisplayURL(photo.ID)
	if err != nil {
		return "", "", err
	}

	details, err := reader.GetPhotoDetails(photo.ID)
	if err != nil {
		return "", "", err
	}

	// EXIF and everything below is supplementary (the "additional
	// information" panel, map, groups, people): each degrades gracefully
	// on its own if it fails, instead of failing the whole publish — e.g.
	// getExif/getFavorites/etc. reject anonymous (unauthenticated) calls,
	// and getGeo "fails" for any non-geotagged photo (the common case).
	exif, _ := reader.GetPhotoExif(photo.ID)
	favorites, _ := reader.GetPhotoFavoritesCount(photo.ID)
	groups, _ := reader.GetPhotoGroups(photo.ID)
	people, _ := reader.GetPhotoPeople(photo.ID)

	var mapEmbedURL string
	if lat, lon, hasGeo, geoErr := reader.GetPhotoGeo(photo.ID); geoErr == nil && hasGeo {
		mapEmbedURL, _ = BuildMapEmbedURL(lat, lon)
	}

	var pageViews string
	if getPageViewsFunc != nil {
		path := fmt.Sprintf("/photos/%s/%s.html", username, photo.ID)
		if _, views, pvErr := getPageViewsFunc(path); pvErr == nil {
			pageViews = strconv.Itoa(views)
		}
	}

	// Fetched before rendering: the page shows the owner's *original*
	// caption (StripOwnSentence removes our own appended sentence, which
	// would be circular to show on the very page that IS the video), and
	// the same value is reused below for the idempotent description check.
	currentDescription, err := reader.GetPhotoDescription(photo.ID)
	if err != nil {
		return "", "", err
	}

	pageHTML, err := sitegen.RenderPhotoPage(template, sitegen.PageData{
		PhotoID:          photo.ID,
		Title:            photo.Title,
		ImageURL:         imageURL,
		VideoURL:         videoURL,
		PhotoPageURL:     fmt.Sprintf("https://www.flickr.com/photos/%s/%s/", username, photo.ID),
		OwnerName:        details.OwnerName,
		OwnerAvatarURL:   details.OwnerAvatarURL,
		OwnerDescription: StripOwnSentence(currentDescription),
		DateTaken:        details.DateTaken,
		DateUploaded:     details.DateUploaded,
		Tags:             details.Tags,
		Views:            details.Views,
		PageViews:        pageViews,
		Comments:         details.Comments,
		Favorites:        favorites,
		LicenseName:      details.LicenseName,
		LicenseURL:       details.LicenseURL,
		MapEmbedURL:      mapEmbedURL,
		Groups:           groups,
		People:           people,
		Camera:           exif.Camera,
		ExposureTime:     exif.ExposureTime,
		FNumber:          exif.FNumber,
		ISO:              exif.ISO,
		FocalLength:      exif.FocalLength,
	})
	if err != nil {
		return "", "", err
	}

	viewerURL, err := publisher.PublishPage(token, pageHTML)
	if err != nil {
		return "", "", err
	}

	if !strings.Contains(currentDescription, viewerURL) {
		descriptionLinkURL := viewerURL + "?utm_source=flickr&utm_medium=description&utm_campaign=motion_photo&utm_content=" + url.QueryEscape(username)
		sentence := motionPhotoSentencePrefix + `<a href="` + html.EscapeString(descriptionLinkURL) + `" rel="noreferrer nofollow">watch the video</a>`
		// StripOwnSentence first: currentDescription may already carry an
		// older version of our own sentence (a previous link that's now
		// stale/dead, e.g. a reprocessed orphaned-video photo) — without
		// this, appending blindly leaves both the dead old sentence and
		// the new one in the description.
		originalCaption := StripOwnSentence(currentDescription)
		newDescription := strings.TrimSpace(originalCaption)
		if newDescription != "" {
			newDescription += "\n\n"
		}
		newDescription += sentence
		if err := writer.SetDescription(photo.ID, newDescription); err != nil {
			return "", "", err
		}
	}

	if err := writer.AddTag(photo.ID, statusTagPrefix+"published"); err != nil {
		return "", "", err
	}
	if err := store.Set(photo.ID, statestore.Record{Status: "published", Token: token}); err != nil {
		return "", "", err
	}
	return "published", viewerURL, nil
}

// Run scansiona tutte le pagine di foto dell'utente fino a limit (limit < 0
// = illimitato), processando ogni foto e stampando lo stato risultante.
func Run(reader Reader, writer Writer, uploader Uploader, publisher Publisher, store statestore.Store, userID, username, template string, limit int) error {
	processed := 0
	page := 1
	for limit < 0 || processed < limit {
		photos, err := reader.GetPhotosPage(userID, page, 100)
		if err != nil {
			return err
		}
		if len(photos) == 0 {
			break
		}
		for _, photo := range photos {
			if limit >= 0 && processed >= limit {
				break
			}
			status, viewerURL, err := ProcessPhoto(reader, writer, uploader, publisher, store, template, username, photo)
			if err != nil {
				return fmt.Errorf("photo %s: %w", photo.ID, err)
			}
			fmt.Printf("%s\t%s\t%s\t%s\t%s\n", time.Now().Format("15:04:05"), photo.ID, status, photo.Title, viewerURL)
			if !strings.HasPrefix(status, "skipped") {
				processed++
			}
		}
		page++
	}
	return nil
}
