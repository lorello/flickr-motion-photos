package flickrclient

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

const (
	// maxCallAttempts: tentativi totali per chiamata REST (1 + 3 retry),
	// con backoff retryBaseDelay, 2x, 4x (2s/4s/8s).
	maxCallAttempts = 4
	retryBaseDelay  = 2 * time.Second

	// Circuit breaker: finestra scorrevole degli ultimi breakerWindow esiti
	// (una chiamata = un esito, dopo i retry). Si apre se, con almeno
	// breakerMinSamples esiti, la quota di fallimenti raggiunge
	// breakerMaxFailureRate.
	breakerWindow         = 20
	breakerMinSamples     = 10
	breakerMaxFailureRate = 0.5
)

// ErrCircuitOpen: troppi fallimenti recenti verso l'API Flickr, il run si
// ferma invece di continuare a martellare. Resta aperto per tutta la vita
// del processo (il run è oneshot): ci pensa il prossimo giro del timer.
var ErrCircuitOpen = errors.New("flickr API circuit breaker open: too many recent failures, aborting run")

// transientError marca un errore ritentabile (rete, timeout, body non-JSON).
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// flickrServiceUnavailable è il codice Flickr 105.
const flickrServiceUnavailable = 105

func isTransient(err error) bool {
	var te *transientError
	if errors.As(err, &te) {
		return true
	}
	var ae *APIError
	return errors.As(err, &ae) && ae.Code == flickrServiceUnavailable
}

type circuitBreaker struct {
	mu      sync.Mutex
	results []bool // true = ok, ultimi breakerWindow esiti
	open    bool
}

var apiBreaker = &circuitBreaker{}

func (b *circuitBreaker) allow() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.open {
		return ErrCircuitOpen
	}
	return nil
}

func (b *circuitBreaker) record(ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.results = append(b.results, ok)
	if len(b.results) > breakerWindow {
		b.results = b.results[1:]
	}
	if b.open || len(b.results) < breakerMinSamples {
		return
	}
	failures := 0
	for _, r := range b.results {
		if !r {
			failures++
		}
	}
	if float64(failures)/float64(len(b.results)) >= breakerMaxFailureRate {
		b.open = true
		fmt.Fprintf(os.Stderr, "[%s] [BREAKER] open: %d/%d recent Flickr calls failed, aborting run\n",
			time.Now().Format("15:04:05"), failures, len(b.results))
	}
}

func (b *circuitBreaker) reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.results, b.open = nil, false
}
