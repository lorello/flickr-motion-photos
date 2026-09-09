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
	Status string `json:"status"` // "checked" | "published" | "lost" | "ready-private"
	// VideoURL è popolato solo per "ready-private": il video è già stato
	// estratto e caricato su R2 mentre la foto era ancora privata, così
	// quando diventerà pubblica non serve riscaricare l'originale da
	// Flickr (costoso, soggetto a rate limit) per finire la pubblicazione.
	VideoURL string `json:"video_url,omitempty"`
	// Token è la chiave R2 casuale (non l'ID Flickr, indovinabile) usata
	// per il video e la pagina di questa foto — generato una sola volta e
	// riusato da ogni run successivo (incluso cmd/republish) così il link
	// già condiviso in descrizione non cambia mai.
	Token string `json:"token,omitempty"`
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
			return nil, fmt.Errorf("corrupt cache file %s: %w", path, err)
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

// RecordsByStatus ritorna, indicizzati per photo ID, i record in cache con
// lo status dato (es. "published"), per strumenti di manutenzione (es.
// cmd/republish) che devono rielaborare tutte le foto già pubblicate senza
// rifare la scansione — serve l'intero Record (non solo l'ID) per poter
// riusare lo stesso Token già assegnato, invece di generarne uno nuovo.
func (s *FileStore) RecordsByStatus(status string) map[string]Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]Record)
	for id, r := range s.data {
		if r.Status == status {
			out[id] = r
		}
	}
	return out
}
