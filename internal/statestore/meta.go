package statestore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// rescanMargin: quanto indietro, rispetto all'ultimo scan completo, si
// riguardano comunque le foto per intercettare cambi di stato su foto già
// caricate (es. privata -> pubblica, tag rimosso a mano).
const rescanMargin = 365 * 24 * time.Hour

// ScanMeta tiene la profondità dello scan, separata dal Record per foto.
type ScanMeta struct {
	// Cutoff: nessuna motion photo è stata caricata prima di questa data,
	// quindi non si scende mai oltre. Zero = nessun limite (scan completo
	// della storia).
	Cutoff time.Time `json:"cutoff"`
	// LastCompleteScan: inizio dell'ultimo run che ha percorso tutta la
	// sua finestra senza errori. Zero = non è mai stato fatto un giro
	// completo.
	LastCompleteScan time.Time `json:"last_complete_scan"`

	path string
}

// LoadMeta apre (o inizializza vuoto) il file meta per userID in dir.
func LoadMeta(dir, userID string) (*ScanMeta, error) {
	m := &ScanMeta{path: filepath.Join(dir, fmt.Sprintf("flickrmp_%s_meta.json", userID))}
	raw, err := os.ReadFile(m.path)
	if os.IsNotExist(err) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, m); err != nil {
		return nil, fmt.Errorf("corrupt meta file %s: %w", m.path, err)
	}
	return m, nil
}

func (m *ScanMeta) Save() error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.path, raw, 0o644)
}

func (m *ScanMeta) Path() string { return m.path }

// Since ritorna da quale data di upload scansionare: tutta la storia fino
// al Cutoff finché non c'è mai stato un giro completo, poi solo
// LastCompleteScan - rescanMargin (mai prima del Cutoff). Zero = nessun
// limite.
func (m *ScanMeta) Since() time.Time {
	if m.LastCompleteScan.IsZero() {
		return m.Cutoff
	}
	since := m.LastCompleteScan.Add(-rescanMargin)
	if since.Before(m.Cutoff) {
		return m.Cutoff
	}
	return since
}
