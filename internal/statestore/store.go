// Package statestore tiene traccia delle foto già processate, per non
// rifare lavoro (chiamate Flickr/estrazione video) tra un run e l'altro.
//
// Implementazione attuale: file JSON in /tmp, con chiave (prefisso di file)
// = FLICKR_USER_ID — così una cache diversa per utente è già naturale se
// domani il tool gira per più account (Fase 2). Store è un'interfaccia
// minimale apposta: sostituirla con un'implementazione Redis (o altro
// backend condiviso, per far girare più istanze in parallelo) significa
// scrivere un nuovo tipo con questi due metodi, senza toccare lo scanner.
package statestore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Record è quello che sappiamo su una foto già processata in un run
// precedente.
type Record struct {
	Status string `json:"status"` // "checked" | "published" | "lost"
}

// Store è l'interfaccia consumata dallo scanner: get/set per photo ID.
// Un'implementazione Redis futura soddisfa la stessa interfaccia.
type Store interface {
	Get(photoID string) (Record, bool)
	Set(photoID string, r Record) error
}

// FileStore è l'implementazione attuale: un file JSON in /tmp, con nome
// prefissato dallo user ID Flickr per isolare le cache di utenti diversi
// (pensato per scalare a multi-utente senza cambiare l'interfaccia).
type FileStore struct {
	path string
	mu   sync.Mutex
	data map[string]Record
}

// NewFileStore apre (o crea) la cache per userID nella directory dir
// (tipicamente os.TempDir()). Il file si chiama "flickrmp_<userID>_state.json".
func NewFileStore(dir, userID string) (*FileStore, error) {
	path := filepath.Join(dir, fmt.Sprintf("flickrmp_%s_state.json", userID))
	data := map[string]Record{}

	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &data); err != nil {
			return nil, fmt.Errorf("cache corrotta in %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	return &FileStore{path: path, data: data}, nil
}

func (s *FileStore) Get(photoID string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data[photoID]
	return r, ok
}

func (s *FileStore) Set(photoID string, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[photoID] = r
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o644)
}

// Path espone il file su disco (utile per log/debug).
func (s *FileStore) Path() string {
	return s.path
}
