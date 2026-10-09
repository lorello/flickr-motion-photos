package scanner

import (
	"strconv"
	"time"

	"motionphotos/internal/statestore"
)

// cutoffMargin: margine sotto la motion photo più vecchia trovata, per non
// tagliare per un soffio (l'ID Flickr ordina solo approssimativamente per
// data di upload).
const cutoffMargin = 30 * 24 * time.Hour

type recordLister interface {
	RecordsByStatus(status string) map[string]statestore.Record
}

type uploadTimeReader interface {
	PhotoUploadTime(photoID string) (time.Time, error)
}

// DeriveCutoff stima da quando esistono motion photo: prende quella con ID
// più basso tra le già riconosciute (published, ready-private, lost) e ne
// legge la data di upload, meno cutoffMargin. Ritorna zero (nessun limite)
// se la cache non ne contiene nessuna.
func DeriveCutoff(store recordLister, reader uploadTimeReader) (time.Time, error) {
	var oldestID string
	var oldestNum int64
	for _, status := range []string{"published", "ready-private", "lost"} {
		for id := range store.RecordsByStatus(status) {
			n, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				continue
			}
			if oldestID == "" || n < oldestNum {
				oldestID, oldestNum = id, n
			}
		}
	}
	if oldestID == "" {
		return time.Time{}, nil
	}
	uploaded, err := reader.PhotoUploadTime(oldestID)
	if err != nil {
		return time.Time{}, err
	}
	return uploaded.Add(-cutoffMargin), nil
}
