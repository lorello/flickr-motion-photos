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
	"errors"
	"fmt"
	"strings"

	"motionphotos/internal/flickrclient"
	"motionphotos/internal/motiondetect"
	"motionphotos/internal/sitegen"
	"motionphotos/internal/statestore"
)

const statusTagPrefix = "flickrmp:status="

// Reader è il sottoinsieme di lettura di flickrclient.Client richiesto dallo
// scanner — separato da Writer così le due responsabilità si possono
// sostituire indipendentemente (es. Reader reale + Writer simulato, come
// in questa fase).
type Reader interface {
	GetPhotosPage(userID string, page, perPage int) ([]flickrclient.Photo, error)
	GetPhotoOriginalBytes(photoID string) ([]byte, error)
	GetPhotoDisplayURL(photoID string) (string, error)
	GetPhotoDescription(photoID string) (string, error)
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
// "published" (esito del rilevamento), "blocked-no-oauth" quando l'originale
// non è scaricabile senza un access token OAuth, o "download-error" per un
// fallimento di rete/CDN transitorio (es. 502 da live.staticflickr.com).
// Nessuno dei due casi di errore è fatale né viene salvato in cache: si
// ritenta al prossimo run.
func ProcessPhoto(reader Reader, writer Writer, uploader Uploader, publisher Publisher, store statestore.Store, template, username string, photo flickrclient.Photo) (string, error) {
	if alreadyTagged(photo.Tags) {
		return "skipped(flickr-tag)", nil
	}
	if _, ok := store.Get(photo.ID); ok {
		return "skipped(cache)", nil
	}

	jpegBytes, err := reader.GetPhotoOriginalBytes(photo.ID)
	if err != nil {
		if errors.Is(err, flickrclient.ErrOriginalNotAvailable) {
			return "blocked-no-oauth", nil
		}
		return "download-error", nil //nolint:nilerr // transitorio (rete/CDN): si ritenta al prossimo run, non è un errore dello scanner
	}

	result := detectMotionPhotoFunc(jpegBytes)

	if !result.IsMotion {
		if err := store.Set(photo.ID, statestore.Record{Status: result.Reason}); err != nil {
			return "", err
		}
		if err := writer.AddTag(photo.ID, statusTagPrefix+result.Reason); err != nil {
			return "", err
		}
		return result.Reason, nil
	}

	videoURL, err := uploader.UploadVideo(photo.ID, result.VideoBytes)
	if err != nil {
		return "", err
	}

	imageURL, err := reader.GetPhotoDisplayURL(photo.ID)
	if err != nil {
		return "", err
	}

	html, err := sitegen.RenderPhotoPage(template, sitegen.PageData{
		PhotoID:      photo.ID,
		Title:        photo.Title,
		ImageURL:     imageURL,
		VideoURL:     videoURL,
		PhotoPageURL: fmt.Sprintf("https://www.flickr.com/photos/%s/%s/", username, photo.ID),
	})
	if err != nil {
		return "", err
	}

	viewerURL, err := publisher.PublishPage(photo.ID, html)
	if err != nil {
		return "", err
	}

	currentDescription, err := reader.GetPhotoDescription(photo.ID)
	if err != nil {
		return "", err
	}
	if !strings.Contains(currentDescription, viewerURL) {
		sentence := "Motion-Photo viewer: questa è una foto motion, guardala su " + viewerURL
		newDescription := strings.TrimSpace(currentDescription + "\n\n" + sentence)
		if err := writer.SetDescription(photo.ID, newDescription); err != nil {
			return "", err
		}
	}

	if err := writer.AddTag(photo.ID, statusTagPrefix+"published"); err != nil {
		return "", err
	}
	if err := store.Set(photo.ID, statestore.Record{Status: "published"}); err != nil {
		return "", err
	}
	return "published", nil
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
			status, err := ProcessPhoto(reader, writer, uploader, publisher, store, template, username, photo)
			if err != nil {
				return fmt.Errorf("photo %s: %w", photo.ID, err)
			}
			fmt.Printf("%s\t%s\t%s\n", photo.ID, status, photo.Title)
			if !strings.HasPrefix(status, "skipped") {
				processed++
			}
		}
		page++
	}
	return nil
}
