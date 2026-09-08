# Motion Photo Viewer per Flickr — Design

Data: 2026-09-07

## Problema

Flickr non mostra le Motion Photo (Pixel/Android): le tratta come JPEG statiche, ignorando il video incorporato. L'utente vuole che, per ogni motion photo pubblica caricata, la descrizione Flickr contenga un link a una pagina web che mostri il video (esperienza simile alla "photo page" full-size di Flickr).

## Validazione tecnica (spike, 2026-09-07)

Testato su foto reale: https://flickr.com/photos/lorello/55391822566/

- `flickr.photos.getSizes` **anonimo** → `candownload: 0`, nessuna size "Original" in lista.
- `flickr.photos.getInfo` **autenticato come owner** (OAuth 1.0a, perms=write) → `candownload: 1`, e i campi `originalsecret`/`originalformat` sono presenti.
  - **Conclusione: il download dell'originale richiede sempre OAuth owner-autenticato**, anche per foto pubbliche proprie. Le chiamate anonime non bastano.
- URL originale: `https://live.staticflickr.com/{server}/{id}_{originalsecret}_o.{originalformat}`
- Scaricato il file originale (6.2MB, JPEG + EXIF Pixel Fold/HDR+ confermato).
- XMP contiene `GCamera:MotionPhoto="1"` — marcatore affidabile.
- In coda al JPEG, dopo l'ultimo EOI, c'è un MP4 completo e valido (verificato con `file`: "ISO Media, MP4 Base Media v1"), struttura `ftyp`+`mdat`+`moov`.
  - **Attenzione offset**: il match testuale della stringa `ftyp` è 4 byte *dopo* l'inizio reale del box MP4 (i primi 4 byte sono la box-size, non testuali). L'estrazione deve partire da `offset_match - 4`.
- **Flickr preserva i byte del video intatti** nell'upload dell'originale. Il video NON viene stripped.

## Architettura — Fase 1 (attuale)

Tutto locale, foto pubbliche, nessuna auth sul lato viewer.

**Tech stack: Go 1.22+** (stdlib per HTTP/OAuth/JSON, `aws-sdk-go-v2` per R2 — vedi piano
implementativo per la motivazione: binario statico singolo, footprint minimo, nessun
runtime/interprete da gestire per un CLI a bassa frequenza d'uso).

```
flickr-motion-photos/
├── go.mod
├── cmd/
│   ├── scanner/main.go     # CLI: scan + detect + extract + upload + tag + edit descrizione
│   └── authorize/main.go   # script one-off OAuth
├── internal/
│   ├── motiondetect/       # rilevamento motion photo + estrazione video
│   ├── flickroauth/        # OAuth 1.0a (request_token → authorize → access_token → firma chiamate)
│   ├── flickrclient/       # client Flickr API di alto livello
│   ├── sitegen/            # rendering pagina viewer
│   ├── r2upload/           # upload video su Cloudflare R2
│   ├── gitpublish/         # commit + push pagine generate
│   └── config/             # config da env vars
├── site/
│   ├── template.html    # template pagina viewer singola foto (stile dark, tipo Flickr photo page)
│   └── p/<photo_id>.html  # generata per ogni motion photo trovata
├── docs/superpowers/specs/  # questo documento
└── config (bucket R2, repo GitHub Pages, api key/secret via Bitwarden)
```

**Auth**: OAuth 1.0a locale, token salvato una volta (Bitwarden "Personal Secrets", coerente col pattern già in uso su questa macchina), usato solo dalla CLI. La pagina pubblicata NON richiede auth: foto e video sono già pubblici.

**Storage stato**: machine tag Flickr, namespace `flickrmp`, predicate `status`:
- `flickrmp:status=checked` — scansionata, non è motion photo
- `flickrmp:status=published` — motion trovata, pagina generata e linkata
- `flickrmp:status=lost` — XMP indica motion ma payload MP4 assente/non recuperabile

Nessun file di stato locale: lo stato vive sull'account Flickr stesso, portabile tra macchine.

### Flusso per ogni run

1. `flickr.people.getPhotos` (autenticato) con `extras=tags`, **`privacy_filter=1`** (solo foto pubbliche — senza, l'account owner-autenticato vedrebbe anche private/friends&family) → lista foto pubbliche + tag già presenti, nessuna chiamata extra per lo stato
2. filtra foto senza tag `flickrmp:status=*`
3. per ogni foto candidata:
   a. `flickr.photos.getInfo` → `originalsecret`/`originalformat`
   b. scarica originale da `live.staticflickr.com`
   c. cerca XMP `GCamera:MotionPhoto` + payload MP4 in coda (vedi offset sopra)
      - non trovato → tag `checked`, skip
      - XMP sì ma MP4 assente → tag `lost`, skip, log warning
      - trovato → estrai MP4
   d. upload video su Cloudflare R2 (key = photo_id, overwrite idempotente, bucket pubblico con CORS aperto per `<video>`)
   e. genera `site/p/<photo_id>.html` da template (foto + video, stile dark Flickr-like)
   f. commit + push → GitHub Pages rebuilda
   g. `flickr.photos.setMeta`: **solo se il link non è già presente in descrizione** (idempotenza), appende `"This is a Motion Photo — watch the video: <url>"` alla descrizione esistente senza toccare testo utente
   h. `flickr.photos.addTags`: aggiunge `flickrmp:status=published` — **ultimo step**, garantisce che un crash a metà non lasci lo stato incoerente (rerun ritenta senza duplicare nulla grazie ai controlli idempotenti sui passi precedenti)

## Error handling

- Errore transitorio in detection (exiftool crash, rete) → nessun tag, ritenta al prossimo run
- Rate limit Flickr (3600 req/h) → backoff, continua da dove interrotto (stato su Flickr, non serve checkpoint locale)
- Privacy "allow download" disabilitata dall'utente stesso → tag `lost`
- Secrets (Flickr key/secret, OAuth token, credenziali R2, token push GitHub) → Bitwarden "Personal Secrets", pattern già stabilito su questa macchina (vedi CLAUDE.md utente)

## Testing

Unit test automatici per package (`go test ./...`, uno per modulo `internal/*`), più validazione manuale end-to-end (script CLI personale, non serve suite e2e automatica):
1. `go run ./cmd/scanner --dry-run --limit 5` su foto note motion → verifica detection
2. run reale su quelle 5 (`go run ./cmd/scanner --limit 5`, o binario compilato) → verifica pagina viewer, tag, descrizione su Flickr
3. rerun stesso batch → verifica idempotenza (no duplicati)
4. uso quotidiano: lancio manuale dopo ogni batch upload da Pixel

## Roadmap — Fase 2 (futura, fuori scope ora)

Per diventare un servizio usabile da altri utenti Flickr (non solo l'account personale):

- **Auth multi-utente**: ogni utente autorizza l'app via OAuth 1.0a, il token viene salvato in un profilo privato (non più locale/Bitwarden, ma storage cloud per-utente)
- **Scansione diventa cloud**: non più CLI locale, ma servizio che scansiona periodicamente le foto di ogni utente registrato (con il proprio token)
- **Permessi Flickr rispettati**: quando utente A visita la pagina viewer di una motion photo di utente B, il servizio deve verificare i permessi Flickr (pubblica/amici/famiglia/privata) per decidere se mostrare il video ad A — richiede che anche il viewer (A) sia autenticato, o quantomeno che il servizio verifichi la visibilità della foto per il contesto di chi guarda
- Implica: gestione multi-tenant dei token, storage per-utente, verifica permessi a runtime invece che generazione statica una-tantum

Questa fase non è progettata in dettaglio qui: richiederà una spec propria quando si arriva a quel punto.
