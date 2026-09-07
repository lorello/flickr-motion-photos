# Motion Photo Viewer per Flickr Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** CLI Go che scansiona le foto pubbliche Flickr dell'utente, rileva le Motion Photo Pixel, estrae il video incorporato, pubblica una pagina viewer statica (foto + video, stile Flickr photo page) e linka quella pagina nella descrizione Flickr della foto.

**Architecture:** Pacchetti `internal/*` puri e testabili in isolamento (detection, oauth signing, client Flickr, generazione pagina, upload video, publish git, config) composti da un orchestratore `internal/scanner` richiamato da un entrypoint sottile `cmd/scanner`. Stato persistito come machine tag Flickr (`flickrmp:status=...`), nessun database locale. Auth OAuth 1.0a solo lato CLI; la pagina pubblicata (GitHub Pages) e il video (Cloudflare R2) restano pubblici senza auth. Dependency injection tramite variabili di package (`var xFunc = pkg.RealFunc`, sovrascritte nei test) invece di interfacce pesanti, dove il collaboratore è una singola funzione.

**Tech Stack:** Go 1.22+ stdlib (`net/http`, `crypto/hmac`, `crypto/sha1`, `encoding/json`, `os/exec` — nessuna libreria OAuth esterna), `github.com/aws/aws-sdk-go-v2` (client S3-compatibile per Cloudflare R2), `testing` stdlib (nessun framework di test esterno), git CLI, gh CLI (GitHub).

**Spec:** `docs/superpowers/specs/2026-09-07-motion-photo-viewer-design.md`

## Global Constraints

- Nessun file di stato locale: lo stato di scansione vive esclusivamente come machine tag Flickr `flickrmp:status={checked|published|lost}` (namespace `flickrmp`, predicate `status`).
- Ogni scrittura verso Flickr/R2/git deve essere idempotente: un rerun sullo stesso batch non deve duplicare link in descrizione, non deve ricaricare il video se già presente (l'upload R2 è comunque un overwrite, quindi già naturalmente idempotente), non deve aggiungere un secondo tag di stato.
- Il tag di stato va scritto **per ultimo**, dopo che descrizione e pagina sono state pubblicate con successo (garanzia di non lasciare stato incoerente in caso di crash a metà run).
- Download dell'originale Flickr richiede sempre OAuth owner-autenticato (validato: chiamata anonima ha `candownload: 0`).
- `flickr.people.getPhotos` **deve** includere `privacy_filter=1`: senza, l'account owner-autenticato vede anche foto private/friends&family, non solo pubbliche — la spec richiede di scansionare solo foto pubbliche.
- Estrazione video: il match testuale `ftyp` è 4 byte dopo l'inizio reale del box MP4 (i 4 byte precedenti sono la box-size). L'estrazione parte da `offset_del_match - 4`.
- Testo appeso alla descrizione Flickr, quando si pubblica una motion photo: `"Motion-Photo viewer: questa è una foto motion, guardala su <url>"` — l'idempotenza si verifica controllando che `<url>` (il viewer URL) non sia già presente nella descrizione corrente.
- Nessuna libreria OAuth esterna: l'algoritmo di firma HMAC-SHA1 è hand-rolled con stdlib (`crypto/hmac`, `crypto/sha1`), validato contro il vettore canonico OAuth 1.0 (`tR3+Ty81lMeYAr/Fid0kMTYa/WM=`).
- Percent-encoding per la base string OAuth: charset unreserved RFC3986 (`A-Z a-z 0-9 - . _ ~`), tutto il resto `%XX` maiuscolo. **Non** usare `url.QueryEscape`/`url.Values.Encode` per questo scopo (form-encoding, spazio→`+`, sbagliato per la base string OAuth) — va bene invece per costruire la query string della richiesta HTTP vera e propria (stesso comportamento di `urllib.parse.urlencode` usato nello spike originale).
- Secrets via variabili d'ambiente, caricate da Bitwarden "Personal Secrets" seguendo il pattern già in uso su questa macchina (vedi `~/CLAUDE.md`, funzione `bw-unlock`).
- Layout Go idiomatico: logica riusabile in `internal/<package>`, entrypoint sottili in `cmd/<binary>/main.go`.
- Testabilità senza mock framework: package con una singola dipendenza esterna a effetto (HTTP, filesystem) espongono una `var xFunc = ...` sovrascrivibile nei test; package con più metodi correlati (es. `flickrclient.Client`) espongono un'interfaccia minimale lato consumer (es. `scanner.FlickrAPI`).

---

## File Structure

```
flickr-motion-photos/
├── go.mod
├── go.sum
├── site/
│   └── template.html
├── internal/
│   ├── motiondetect/
│   │   ├── detect.go
│   │   └── detect_test.go
│   ├── flickroauth/
│   │   ├── oauth.go
│   │   └── oauth_test.go
│   ├── flickrclient/
│   │   ├── client.go
│   │   └── client_test.go
│   ├── sitegen/
│   │   ├── generator.go
│   │   └── generator_test.go
│   ├── r2upload/
│   │   ├── upload.go
│   │   └── upload_test.go
│   ├── gitpublish/
│   │   ├── publish.go
│   │   └── publish_test.go
│   ├── config/
│   │   └── config.go
│   └── scanner/
│       ├── scanner.go
│       └── scanner_test.go
└── cmd/
    ├── scanner/
    │   └── main.go
    └── authorize/
        └── main.go
```

---

### Task 1: Go module init + motion photo detection

**Files:**
- Create: `go.mod`
- Create: `internal/motiondetect/detect.go`
- Test: `internal/motiondetect/detect_test.go`

**Interfaces:**
- Produces: `Result` struct (`IsMotion bool`, `VideoBytes []byte`, `Reason string` con valori `"published"|"checked"|"lost"`), funzione `DetectMotionPhoto(jpegBytes []byte) Result`. Usato da Task 7 (`internal/scanner`) e corrisponde 1:1 ai valori del machine tag `flickrmp:status=<reason>`.

- [ ] **Step 1: Inizializza il modulo Go**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
go mod init motionphotos
```

Expected: crea `go.mod` con `module motionphotos` e `go 1.22` (o versione installata).

- [ ] **Step 2: Write the failing tests**

```go
// internal/motiondetect/detect_test.go
package motiondetect

import (
	"bytes"
	"testing"
)

var xmpMarker = []byte(`xmlns:GCamera="http://ns.google.com/photos/1.0/camera/" GCamera:MotionPhoto="1"`)

func fakeMP4Payload() []byte {
	return []byte("\x00\x00\x00\x18ftypisom\x00\x02\x00\x00isomiso2mp41FAKEMDATBYTES")
}

func TestPublishedWhenXMPAndVideoPresent(t *testing.T) {
	jpeg := bytes.Join([][]byte{
		[]byte("JFIF-fake-header"),
		xmpMarker,
		[]byte("restofjpegdata\xff\xd9"),
		fakeMP4Payload(),
	}, nil)

	result := DetectMotionPhoto(jpeg)

	if !result.IsMotion {
		t.Fatal("expected IsMotion true")
	}
	if result.Reason != "published" {
		t.Fatalf("expected reason published, got %s", result.Reason)
	}
	if !bytes.HasPrefix(result.VideoBytes, []byte("\x00\x00\x00\x18ftypisom")) {
		t.Fatalf("expected video bytes to start with ftyp box, got %v", result.VideoBytes[:20])
	}
}

func TestCheckedWhenNoMotionMarkersAtAll(t *testing.T) {
	jpeg := []byte("JFIF-fake-header-plain-photo-no-markers\xff\xd9")

	result := DetectMotionPhoto(jpeg)

	if result.IsMotion {
		t.Fatal("expected IsMotion false")
	}
	if result.Reason != "checked" {
		t.Fatalf("expected reason checked, got %s", result.Reason)
	}
	if result.VideoBytes != nil {
		t.Fatal("expected nil video bytes")
	}
}

func TestLostWhenXMPPresentButVideoMissing(t *testing.T) {
	jpeg := bytes.Join([][]byte{
		[]byte("JFIF-fake-header"),
		xmpMarker,
		[]byte("restofjpegdata\xff\xd9"),
	}, nil)

	result := DetectMotionPhoto(jpeg)

	if result.IsMotion {
		t.Fatal("expected IsMotion false")
	}
	if result.Reason != "lost" {
		t.Fatalf("expected reason lost, got %s", result.Reason)
	}
	if result.VideoBytes != nil {
		t.Fatal("expected nil video bytes")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && go test ./internal/motiondetect/... -v`
Expected: FAIL — `undefined: DetectMotionPhoto` (il package non ha ancora implementazione)

- [ ] **Step 4: Write the implementation**

```go
// internal/motiondetect/detect.go
package motiondetect

import "bytes"

const (
	xmpMotionMarker  = `GCamera:MotionPhoto="1"`
	mp4BoxTypeMarker = "ftyp"
	boxSizeFieldLen  = 4
)

// Result descrive l'esito del rilevamento su un file JPEG.
type Result struct {
	IsMotion   bool
	VideoBytes []byte
	Reason     string // "published" | "checked" | "lost"
}

func findEmbeddedVideo(jpegBytes []byte) []byte {
	idx := bytes.Index(jpegBytes, []byte(mp4BoxTypeMarker))
	if idx < boxSizeFieldLen {
		return nil
	}
	boxStart := idx - boxSizeFieldLen
	video := jpegBytes[boxStart:]
	if len(video) < 8 {
		return nil
	}
	return video
}

// DetectMotionPhoto cerca il marcatore XMP GCamera:MotionPhoto e un payload
// MP4 in coda al JPEG. Il match testuale di "ftyp" cade 4 byte dopo l'inizio
// reale del box (i 4 byte precedenti sono la box-size, non testuali).
func DetectMotionPhoto(jpegBytes []byte) Result {
	hasXMPFlag := bytes.Contains(jpegBytes, []byte(xmpMotionMarker))
	video := findEmbeddedVideo(jpegBytes)

	if video != nil {
		return Result{IsMotion: true, VideoBytes: video, Reason: "published"}
	}
	if hasXMPFlag {
		return Result{IsMotion: false, VideoBytes: nil, Reason: "lost"}
	}
	return Result{IsMotion: false, VideoBytes: nil, Reason: "checked"}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && go test ./internal/motiondetect/... -v`
Expected: 3 passed

- [ ] **Step 6: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add go.mod internal/motiondetect/
git commit -m "feat: motion photo detection from JPEG+embedded MP4 bytes"
```

---

### Task 2: OAuth 1.0a signing

**Files:**
- Create: `internal/flickroauth/oauth.go`
- Test: `internal/flickroauth/oauth_test.go`

**Interfaces:**
- Produces:
  - `Sign(method, rawURL string, params map[string]string, consumerSecret, tokenSecret string) string`
  - `BuildOAuthParams(consumerKey, token string) map[string]string` (`token == ""` significa "nessun token", non c'è `Optional` in Go)
  - `SignedGet(rawURL string, params map[string]string, consumerKey, consumerSecret, token, tokenSecret string) (string, error)` (esegue la richiesta HTTP e ritorna il body come stringa)
  - `GetRequestToken(consumerKey, consumerSecret string) (map[string]string, error)` (`{"oauth_token", "oauth_token_secret"}`)
  - `BuildAuthorizeURL(requestToken, perms string) string`
  - `GetAccessToken(consumerKey, consumerSecret, requestToken, requestTokenSecret, verifier string) (map[string]string, error)` (`{"oauth_token", "oauth_token_secret", "user_nsid", "username"}`)
- Consumato da Task 3 (`internal/flickrclient`) e da Task 8 (`cmd/authorize`).

- [ ] **Step 1: Write the failing test**

Il vettore usato sotto è l'esempio canonico OAuth 1.0 (consumer/token/secret/nonce/timestamp fissi), già verificato manualmente in una sessione precedente contro questa stessa implementazione (in Python) durante lo spike di design: produce `tR3+Ty81lMeYAr/Fid0kMTYa/WM=`. La logica di firma è identica, solo la sintassi cambia.

```go
// internal/flickroauth/oauth_test.go
package flickroauth

import "testing"

func TestSignMatchesCanonicalOAuth1Vector(t *testing.T) {
	params := map[string]string{
		"oauth_consumer_key":     "dpf43f3p2l4k3l03",
		"oauth_token":            "nnch734d00sl2jdk",
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_timestamp":        "1191242096",
		"oauth_nonce":            "kllo9940pd9333jh",
		"oauth_version":          "1.0",
		"file":                   "vacation.jpg",
		"size":                   "original",
	}
	sig := Sign("GET", "http://photos.example.net/photos", params, "kd94hf93k423kf44", "pfkkdhi9sl3r4s00")
	want := "tR3+Ty81lMeYAr/Fid0kMTYa/WM="
	if sig != want {
		t.Fatalf("got %s, want %s", sig, want)
	}
}

func TestBuildOAuthParamsWithoutTokenHasNoTokenField(t *testing.T) {
	p := BuildOAuthParams("consumer-key-x", "")
	if p["oauth_consumer_key"] != "consumer-key-x" {
		t.Fatalf("unexpected consumer key: %s", p["oauth_consumer_key"])
	}
	if p["oauth_signature_method"] != "HMAC-SHA1" {
		t.Fatalf("unexpected signature method: %s", p["oauth_signature_method"])
	}
	if p["oauth_version"] != "1.0" {
		t.Fatalf("unexpected version: %s", p["oauth_version"])
	}
	if _, ok := p["oauth_token"]; ok {
		t.Fatal("expected no oauth_token field")
	}
	if len(p["oauth_nonce"]) != 32 {
		t.Fatalf("expected 32-char nonce, got %d", len(p["oauth_nonce"]))
	}
}

func TestBuildOAuthParamsWithTokenIncludesIt(t *testing.T) {
	p := BuildOAuthParams("consumer-key-x", "req-token-abc")
	if p["oauth_token"] != "req-token-abc" {
		t.Fatalf("expected oauth_token req-token-abc, got %s", p["oauth_token"])
	}
}

func TestBuildAuthorizeURLIncludesTokenAndPerms(t *testing.T) {
	url := BuildAuthorizeURL("req-token-123", "write")
	want := "https://www.flickr.com/services/oauth/authorize?oauth_token=req-token-123&perms=write"
	if url != want {
		t.Fatalf("got %s, want %s", url, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && go test ./internal/flickroauth/... -v`
Expected: FAIL — `undefined: Sign` (nessuna implementazione ancora)

- [ ] **Step 3: Write the implementation**

```go
// internal/flickroauth/oauth.go
package flickroauth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	requestTokenURL = "https://www.flickr.com/services/oauth/request_token"
	authorizeURL    = "https://www.flickr.com/services/oauth/authorize"
	accessTokenURL  = "https://www.flickr.com/services/oauth/access_token"
	nonceAlphabet   = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

// httpGetFunc permette ai test di sostituire la richiesta HTTP reale.
var httpGetFunc = http.Get

func isUnreserved(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
		c == '-' || c == '.' || c == '_' || c == '~'
}

// percentEncode implementa il percent-encoding RFC3986 richiesto da OAuth 1.0a
// per la base string (equivalente a Python urllib.parse.quote(s, safe='')).
// url.QueryEscape NON va bene qui: usa form-encoding (spazio -> '+').
func percentEncode(s string) string {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isUnreserved(c) {
			sb.WriteByte(c)
		} else {
			fmt.Fprintf(&sb, "%%%02X", c)
		}
	}
	return sb.String()
}

func nonce(length int) string {
	buf := make([]byte, length)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	out := make([]byte, length)
	for i, v := range buf {
		out[i] = nonceAlphabet[int(v)%len(nonceAlphabet)]
	}
	return string(out)
}

// Sign calcola la firma HMAC-SHA1 OAuth 1.0a per method/url/params.
func Sign(method, rawURL string, params map[string]string, consumerSecret, tokenSecret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, percentEncode(k)+"="+percentEncode(params[k]))
	}
	baseParams := strings.Join(pairs, "&")

	baseString := strings.Join([]string{
		strings.ToUpper(method),
		percentEncode(rawURL),
		percentEncode(baseParams),
	}, "&")

	key := percentEncode(consumerSecret) + "&" + percentEncode(tokenSecret)

	mac := hmac.New(sha1.New, []byte(key))
	mac.Write([]byte(baseString))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// BuildOAuthParams costruisce i parametri oauth_* standard. token == "" omette
// oauth_token dal risultato (equivalente a Optional[str] = None in Python).
func BuildOAuthParams(consumerKey, token string) map[string]string {
	params := map[string]string{
		"oauth_nonce":            nonce(32),
		"oauth_timestamp":        strconv.FormatInt(time.Now().Unix(), 10),
		"oauth_consumer_key":     consumerKey,
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_version":          "1.0",
	}
	if token != "" {
		params["oauth_token"] = token
	}
	return params
}

// SignedGet firma e invia una GET, ritornando il body come stringa.
func SignedGet(rawURL string, params map[string]string, consumerKey, consumerSecret, token, tokenSecret string) (string, error) {
	sig := Sign(http.MethodGet, rawURL, params, consumerSecret, tokenSecret)

	query := url.Values{}
	for k, v := range params {
		query.Set(k, v)
	}
	query.Set("oauth_signature", sig)

	resp, err := httpGetFunc(rawURL + "?" + query.Encode())
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func parseQueryToMap(body string) (map[string]string, error) {
	values, err := url.ParseQuery(body)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(values))
	for k := range values {
		out[k] = values.Get(k)
	}
	return out, nil
}

func GetRequestToken(consumerKey, consumerSecret string) (map[string]string, error) {
	params := BuildOAuthParams(consumerKey, "")
	params["oauth_callback"] = "oob"
	body, err := SignedGet(requestTokenURL, params, consumerKey, consumerSecret, "", "")
	if err != nil {
		return nil, err
	}
	return parseQueryToMap(body)
}

func BuildAuthorizeURL(requestToken, perms string) string {
	return fmt.Sprintf("%s?oauth_token=%s&perms=%s", authorizeURL, requestToken, perms)
}

func GetAccessToken(consumerKey, consumerSecret, requestToken, requestTokenSecret, verifier string) (map[string]string, error) {
	params := BuildOAuthParams(consumerKey, requestToken)
	params["oauth_verifier"] = verifier
	body, err := SignedGet(accessTokenURL, params, consumerKey, consumerSecret, requestToken, requestTokenSecret)
	if err != nil {
		return nil, err
	}
	return parseQueryToMap(body)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && go test ./internal/flickroauth/... -v`
Expected: 4 passed

- [ ] **Step 5: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add internal/flickroauth/
git commit -m "feat: OAuth 1.0a signing and token exchange for Flickr API"
```

---

### Task 3: Flickr API client

**Files:**
- Create: `internal/flickrclient/client.go`
- Test: `internal/flickrclient/client_test.go`

**Interfaces:**
- Consumes: `flickroauth.SignedGet`, `flickroauth.BuildOAuthParams` (Task 2)
- Produces:
  - `type APIError struct{ Message string }` con `Error() string`
  - `type Photo struct { ID, Title string; Tags []string }`
  - `NewClient(consumerKey, consumerSecret, accessToken, accessTokenSecret string) *Client`
  - `(*Client).Call(method string, params map[string]string) (map[string]interface{}, error)`
  - `(*Client).GetPhotosPage(page, perPage int) ([]Photo, error)` — include sempre `privacy_filter=1`
  - `(*Client).GetPhotoOriginalURL(photoID string) (string, error)`
  - `(*Client).GetPhotoOriginalBytes(photoID string) ([]byte, error)`
  - `(*Client).GetPhotoDescription(photoID string) (string, error)`
  - `(*Client).SetDescription(photoID, description string) error`
  - `(*Client).AddTag(photoID, tag string) error`
- Consumato da Task 7 (`internal/scanner`), tramite l'interfaccia minimale `scanner.FlickrAPI` che questi metodi soddisfano per struttura.

- [ ] **Step 1: Write the failing tests**

```go
// internal/flickrclient/client_test.go
package flickrclient

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func makeClient() *Client {
	return NewClient("ck", "cs", "at", "ats")
}

func withSignedGet(t *testing.T, fn func(rawURL string, params map[string]string, consumerKey, consumerSecret, token, tokenSecret string) (string, error)) {
	t.Helper()
	orig := signedGetFunc
	signedGetFunc = fn
	t.Cleanup(func() { signedGetFunc = orig })
}

func jsonBody(v map[string]interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestCallRaisesOnFailStatus(t *testing.T) {
	withSignedGet(t, func(string, map[string]string, string, string, string, string) (string, error) {
		return jsonBody(map[string]interface{}{"stat": "fail", "code": 1, "message": "boom"}), nil
	})
	client := makeClient()
	_, err := client.Call("flickr.test.echo", nil)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected error 'boom', got %v", err)
	}
}

func TestCallReturnsParsedJSONOnSuccess(t *testing.T) {
	withSignedGet(t, func(string, map[string]string, string, string, string, string) (string, error) {
		return jsonBody(map[string]interface{}{"stat": "ok", "value": 42}), nil
	})
	client := makeClient()
	result, err := client.Call("flickr.test.echo", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["value"].(float64) != 42 {
		t.Fatalf("unexpected value: %v", result["value"])
	}
}

func TestGetPhotosPageExtractsIDTitleTagsAndFiltersPrivate(t *testing.T) {
	var capturedParams map[string]string
	withSignedGet(t, func(_ string, params map[string]string, _, _, _, _ string) (string, error) {
		capturedParams = params
		return jsonBody(map[string]interface{}{
			"stat": "ok",
			"photos": map[string]interface{}{
				"photo": []interface{}{
					map[string]interface{}{"id": "111", "title": "PXL_1.MP", "tags": "flickrmp:status=checked vacation"},
					map[string]interface{}{"id": "222", "title": "PXL_2.MP", "tags": ""},
				},
			},
		}), nil
	})
	client := makeClient()
	photos, err := client.GetPhotosPage(1, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []Photo{
		{ID: "111", Title: "PXL_1.MP", Tags: []string{"flickrmp:status=checked", "vacation"}},
		{ID: "222", Title: "PXL_2.MP", Tags: []string{}},
	}
	if !reflect.DeepEqual(photos, want) {
		t.Fatalf("got %+v, want %+v", photos, want)
	}
	if capturedParams["privacy_filter"] != "1" {
		t.Fatalf("expected privacy_filter=1, got %q", capturedParams["privacy_filter"])
	}
}

func TestGetPhotoOriginalURLBuildsStaticFlickrURL(t *testing.T) {
	withSignedGet(t, func(string, map[string]string, string, string, string, string) (string, error) {
		return jsonBody(map[string]interface{}{
			"stat":  "ok",
			"photo": map[string]interface{}{"id": "999", "server": "65535", "originalsecret": "abc123", "originalformat": "jpg"},
		}), nil
	})
	client := makeClient()
	url, err := client.GetPhotoOriginalURL("999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://live.staticflickr.com/65535/999_abc123_o.jpg"
	if url != want {
		t.Fatalf("got %s, want %s", url, want)
	}
}

func TestGetPhotoOriginalBytesDownloadsFromStaticURL(t *testing.T) {
	withSignedGet(t, func(string, map[string]string, string, string, string, string) (string, error) {
		return jsonBody(map[string]interface{}{
			"stat":  "ok",
			"photo": map[string]interface{}{"id": "999", "server": "65535", "originalsecret": "abc123", "originalformat": "jpg"},
		}), nil
	})

	var calledURL string
	origHTTPGet := httpGetFunc
	httpGetFunc = func(url string) (*http.Response, error) {
		calledURL = url
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("JPEGBYTES"))}, nil
	}
	t.Cleanup(func() { httpGetFunc = origHTTPGet })

	client := makeClient()
	data, err := client.GetPhotoOriginalBytes("999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "JPEGBYTES" {
		t.Fatalf("got %q", data)
	}
	if calledURL != "https://live.staticflickr.com/65535/999_abc123_o.jpg" {
		t.Fatalf("unexpected URL called: %s", calledURL)
	}
}

func TestGetPhotoDescriptionReadsFromGetInfo(t *testing.T) {
	withSignedGet(t, func(string, map[string]string, string, string, string, string) (string, error) {
		return jsonBody(map[string]interface{}{
			"stat":  "ok",
			"photo": map[string]interface{}{"description": map[string]interface{}{"_content": "Una bella foto"}},
		}), nil
	})
	client := makeClient()
	desc, err := client.GetPhotoDescription("999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if desc != "Una bella foto" {
		t.Fatalf("got %q", desc)
	}
}

func TestSetDescriptionCallsSetMetaWithTitleAndDescription(t *testing.T) {
	calls := 0
	var secondParams map[string]string
	withSignedGet(t, func(_ string, params map[string]string, _, _, _, _ string) (string, error) {
		calls++
		if calls == 1 {
			return jsonBody(map[string]interface{}{
				"stat":  "ok",
				"photo": map[string]interface{}{"title": map[string]interface{}{"_content": "PXL_1.MP"}, "description": map[string]interface{}{"_content": ""}},
			}), nil
		}
		secondParams = params
		return jsonBody(map[string]interface{}{"stat": "ok"}), nil
	})
	client := makeClient()
	if err := client.SetDescription("999", "nuova descrizione"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if secondParams["method"] != "flickr.photos.setMeta" {
		t.Fatalf("unexpected method: %s", secondParams["method"])
	}
	if secondParams["description"] != "nuova descrizione" {
		t.Fatalf("unexpected description: %s", secondParams["description"])
	}
	if secondParams["title"] != "PXL_1.MP" {
		t.Fatalf("unexpected title: %s", secondParams["title"])
	}
}

func TestAddTagCallsAddTags(t *testing.T) {
	var capturedParams map[string]string
	withSignedGet(t, func(_ string, params map[string]string, _, _, _, _ string) (string, error) {
		capturedParams = params
		return jsonBody(map[string]interface{}{"stat": "ok"}), nil
	})
	client := makeClient()
	if err := client.AddTag("999", "flickrmp:status=published"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedParams["method"] != "flickr.photos.addTags" {
		t.Fatalf("unexpected method: %s", capturedParams["method"])
	}
	if capturedParams["photo_id"] != "999" {
		t.Fatalf("unexpected photo_id: %s", capturedParams["photo_id"])
	}
	if capturedParams["tags"] != `"flickrmp:status=published"` {
		t.Fatalf("unexpected tags: %s", capturedParams["tags"])
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && go test ./internal/flickrclient/... -v`
Expected: FAIL — `undefined: NewClient` (nessuna implementazione ancora)

- [ ] **Step 3: Write the implementation**

```go
// internal/flickrclient/client.go
package flickrclient

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"motionphotos/internal/flickroauth"
)

const apiURL = "https://api.flickr.com/services/rest"

// signedGetFunc e httpGetFunc permettono ai test di sostituire il livello HTTP.
var signedGetFunc = flickroauth.SignedGet
var httpGetFunc = http.Get

type APIError struct {
	Message string
}

func (e *APIError) Error() string { return e.Message }

type Photo struct {
	ID    string
	Title string
	Tags  []string
}

type Client struct {
	ConsumerKey       string
	ConsumerSecret    string
	AccessToken       string
	AccessTokenSecret string
}

func NewClient(consumerKey, consumerSecret, accessToken, accessTokenSecret string) *Client {
	return &Client{consumerKey, consumerSecret, accessToken, accessTokenSecret}
}

func (c *Client) Call(method string, params map[string]string) (map[string]interface{}, error) {
	full := flickroauth.BuildOAuthParams(c.ConsumerKey, c.AccessToken)
	full["method"] = method
	full["format"] = "json"
	full["nojsoncallback"] = "1"
	for k, v := range params {
		full[k] = v
	}

	body, err := signedGetFunc(apiURL, full, c.ConsumerKey, c.ConsumerSecret, c.AccessToken, c.AccessTokenSecret)
	if err != nil {
		return nil, err
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(body), &result); err != nil {
		return nil, err
	}
	if stat, _ := result["stat"].(string); stat != "ok" {
		msg, _ := result["message"].(string)
		if msg == "" {
			msg = "unknown Flickr API error"
		}
		return nil, &APIError{Message: msg}
	}
	return result, nil
}

func (c *Client) getPhotoInfo(photoID string) (map[string]interface{}, error) {
	result, err := c.Call("flickr.photos.getInfo", map[string]string{"photo_id": photoID})
	if err != nil {
		return nil, err
	}
	photo, _ := result["photo"].(map[string]interface{})
	return photo, nil
}

// GetPhotosPage restituisce le foto pubbliche dell'utente autenticato.
// privacy_filter=1 e' obbligatorio: senza, l'owner autenticato vedrebbe
// anche foto private/friends&family.
func (c *Client) GetPhotosPage(page, perPage int) ([]Photo, error) {
	result, err := c.Call("flickr.people.getPhotos", map[string]string{
		"user_id":        "me",
		"extras":         "tags",
		"privacy_filter": "1",
		"page":           strconv.Itoa(page),
		"per_page":       strconv.Itoa(perPage),
	})
	if err != nil {
		return nil, err
	}

	photosObj, _ := result["photos"].(map[string]interface{})
	rawList, _ := photosObj["photo"].([]interface{})

	photos := make([]Photo, 0, len(rawList))
	for _, item := range rawList {
		p, _ := item.(map[string]interface{})
		id, _ := p["id"].(string)
		title, _ := p["title"].(string)
		rawTags, _ := p["tags"].(string)
		tags := []string{}
		if rawTags != "" {
			tags = strings.Split(rawTags, " ")
		}
		photos = append(photos, Photo{ID: id, Title: title, Tags: tags})
	}
	return photos, nil
}

func (c *Client) GetPhotoOriginalURL(photoID string) (string, error) {
	info, err := c.getPhotoInfo(photoID)
	if err != nil {
		return "", err
	}
	server, _ := info["server"].(string)
	id, _ := info["id"].(string)
	secret, _ := info["originalsecret"].(string)
	format, _ := info["originalformat"].(string)
	return fmt.Sprintf("https://live.staticflickr.com/%s/%s_%s_o.%s", server, id, secret, format), nil
}

func (c *Client) GetPhotoOriginalBytes(photoID string) ([]byte, error) {
	url, err := c.GetPhotoOriginalURL(photoID)
	if err != nil {
		return nil, err
	}
	resp, err := httpGetFunc(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (c *Client) GetPhotoDescription(photoID string) (string, error) {
	info, err := c.getPhotoInfo(photoID)
	if err != nil {
		return "", err
	}
	desc, _ := info["description"].(map[string]interface{})
	content, _ := desc["_content"].(string)
	return content, nil
}

func (c *Client) SetDescription(photoID, description string) error {
	info, err := c.getPhotoInfo(photoID)
	if err != nil {
		return err
	}
	titleObj, _ := info["title"].(map[string]interface{})
	title, _ := titleObj["_content"].(string)
	_, err = c.Call("flickr.photos.setMeta", map[string]string{
		"photo_id":    photoID,
		"title":       title,
		"description": description,
	})
	return err
}

func (c *Client) AddTag(photoID, tag string) error {
	_, err := c.Call("flickr.photos.addTags", map[string]string{
		"photo_id": photoID,
		"tags":     "\"" + tag + "\"",
	})
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && go test ./internal/flickrclient/... -v`
Expected: 8 passed

- [ ] **Step 5: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add internal/flickrclient/
git commit -m "feat: Flickr API client for photo listing, original download, tags, description"
```

---

### Task 4: Site page generator

**Files:**
- Create: `internal/sitegen/generator.go`
- Create: `site/template.html`
- Test: `internal/sitegen/generator_test.go`

**Interfaces:**
- Produces:
  - `type PageData struct { PhotoID, Title, ImageURL, VideoURL, PhotoPageURL string }`
  - `RenderPhotoPage(template string, data PageData) string`
  - `WritePhotoPage(siteDir, photoID, html string) (string, error)`
- Consumato da Task 7 (`internal/scanner`).

- [ ] **Step 1: Write the failing tests**

```go
// internal/sitegen/generator_test.go
package sitegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testTemplate = `<!doctype html>
<title>{{TITLE}}</title>
<img src="{{IMAGE_URL}}">
<video src="{{VIDEO_URL}}" autoplay muted loop playsinline></video>
<a href="{{PHOTO_PAGE_URL}}">Vedi su Flickr</a>
`

func TestRenderPhotoPageSubstitutesAllPlaceholders(t *testing.T) {
	html := RenderPhotoPage(testTemplate, PageData{
		PhotoID:      "123",
		Title:        "PXL_20260711.MP",
		ImageURL:     "https://live.staticflickr.com/x/123_o.jpg",
		VideoURL:     "https://videos.example.com/123.mp4",
		PhotoPageURL: "https://www.flickr.com/photos/lorello/123/",
	})
	if strings.Contains(html, "{{") {
		t.Fatalf("unresolved placeholder in output: %s", html)
	}
	for _, want := range []string{
		"PXL_20260711.MP",
		"https://live.staticflickr.com/x/123_o.jpg",
		"https://videos.example.com/123.mp4",
		"https://www.flickr.com/photos/lorello/123/",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected output to contain %q", want)
		}
	}
}

func TestWritePhotoPageWritesToSitePPhotoIDHtml(t *testing.T) {
	siteDir := t.TempDir()
	resultPath, err := WritePhotoPage(siteDir, "123", "<html>ciao</html>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantPath := filepath.Join(siteDir, "p", "123.html")
	if resultPath != wantPath {
		t.Fatalf("got %s, want %s", resultPath, wantPath)
	}
	content, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("unexpected error reading file: %v", err)
	}
	if string(content) != "<html>ciao</html>" {
		t.Fatalf("unexpected content: %s", content)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && go test ./internal/sitegen/... -v`
Expected: FAIL — `undefined: RenderPhotoPage`

- [ ] **Step 3: Write the implementation**

```go
// internal/sitegen/generator.go
package sitegen

import (
	"os"
	"path/filepath"
	"strings"
)

type PageData struct {
	PhotoID      string
	Title        string
	ImageURL     string
	VideoURL     string
	PhotoPageURL string
}

func RenderPhotoPage(template string, data PageData) string {
	replacer := strings.NewReplacer(
		"{{TITLE}}", data.Title,
		"{{IMAGE_URL}}", data.ImageURL,
		"{{VIDEO_URL}}", data.VideoURL,
		"{{PHOTO_PAGE_URL}}", data.PhotoPageURL,
	)
	return replacer.Replace(template)
}

func WritePhotoPage(siteDir, photoID, html string) (string, error) {
	pagesDir := filepath.Join(siteDir, "p")
	if err := os.MkdirAll(pagesDir, 0o755); err != nil {
		return "", err
	}
	outputPath := filepath.Join(pagesDir, photoID+".html")
	if err := os.WriteFile(outputPath, []byte(html), 0o644); err != nil {
		return "", err
	}
	return outputPath, nil
}
```

Template ispirato alla Flickr photo page: sfondo scuro, foto/video centrati, titolo e link di ritorno.

```html
<!-- site/template.html -->
<!doctype html>
<html lang="it">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{TITLE}}</title>
<style>
  :root { color-scheme: dark; }
  body {
    margin: 0;
    background: #131313;
    color: #eee;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    display: flex;
    flex-direction: column;
    align-items: center;
    min-height: 100vh;
  }
  header {
    width: 100%;
    padding: 16px 24px;
    box-sizing: border-box;
    text-align: left;
  }
  header a {
    color: #0063dc;
    text-decoration: none;
    font-weight: 600;
  }
  main {
    display: flex;
    flex: 1;
    align-items: center;
    justify-content: center;
    width: 100%;
    padding: 0 16px 32px;
    box-sizing: border-box;
  }
  .stage {
    position: relative;
    max-width: 100%;
  }
  .stage img, .stage video {
    max-width: 100%;
    max-height: 82vh;
    display: block;
    border-radius: 4px;
  }
  .stage video {
    position: absolute;
    top: 0;
    left: 0;
    width: 100%;
    height: 100%;
    object-fit: contain;
    border-radius: 4px;
  }
  h1 {
    font-size: 1rem;
    font-weight: 400;
    color: #bbb;
    margin: 16px 0 0;
    text-align: center;
  }
</style>
</head>
<body>
  <header><a href="{{PHOTO_PAGE_URL}}">&larr; Vedi su Flickr</a></header>
  <main>
    <div class="stage">
      <img src="{{IMAGE_URL}}" alt="{{TITLE}}">
      <video src="{{VIDEO_URL}}" autoplay muted loop playsinline></video>
    </div>
  </main>
  <h1>{{TITLE}}</h1>
</body>
</html>
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && go test ./internal/sitegen/... -v`
Expected: 2 passed

- [ ] **Step 5: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add internal/sitegen/ site/template.html
git commit -m "feat: static viewer page generator with Flickr-like dark template"
```

---

### Task 5: Cloudflare R2 video upload

**Files:**
- Create: `internal/r2upload/upload.go`
- Test: `internal/r2upload/upload_test.go`

**Interfaces:**
- Produces:
  - `type PutObjectAPI interface { PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) }`
  - `NewR2Client(ctx context.Context, accountID, accessKeyID, secretAccessKey string) (*s3.Client, error)`
  - `UploadVideo(ctx context.Context, client PutObjectAPI, bucket, publicBaseURL, photoID string, videoBytes []byte) (string, error)` — ritorna l'URL pubblico
- Consumato da Task 7 (`internal/scanner`).

- [ ] **Step 1: Aggiungi la dipendenza AWS SDK v2**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
go get github.com/aws/aws-sdk-go-v2/aws
go get github.com/aws/aws-sdk-go-v2/config
go get github.com/aws/aws-sdk-go-v2/credentials
go get github.com/aws/aws-sdk-go-v2/service/s3
```

Expected: `go.mod`/`go.sum` aggiornati con le ultime versioni compatibili.

- [ ] **Step 2: Write the failing tests**

```go
// internal/r2upload/upload_test.go
package r2upload

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type fakePutObjectAPI struct {
	gotInput *s3.PutObjectInput
}

func (f *fakePutObjectAPI) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.gotInput = params
	return &s3.PutObjectOutput{}, nil
}

func TestUploadVideoPutsObjectWithCorrectKeyAndContentType(t *testing.T) {
	fake := &fakePutObjectAPI{}
	url, err := UploadVideo(context.Background(), fake, "motion-photos", "https://videos.example.com", "12345", []byte("FAKEVIDEO"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *fake.gotInput.Bucket != "motion-photos" {
		t.Fatalf("unexpected bucket: %s", *fake.gotInput.Bucket)
	}
	if *fake.gotInput.Key != "12345.mp4" {
		t.Fatalf("unexpected key: %s", *fake.gotInput.Key)
	}
	if *fake.gotInput.ContentType != "video/mp4" {
		t.Fatalf("unexpected content type: %s", *fake.gotInput.ContentType)
	}
	body, _ := io.ReadAll(fake.gotInput.Body)
	if !bytes.Equal(body, []byte("FAKEVIDEO")) {
		t.Fatalf("unexpected body: %s", body)
	}
	if url != "https://videos.example.com/12345.mp4" {
		t.Fatalf("unexpected url: %s", url)
	}
}

func TestNewR2ClientConfiguresCorrectEndpoint(t *testing.T) {
	client, err := NewR2Client(context.Background(), "acct123", "key", "secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	opts := client.Options()
	if opts.Region != "auto" {
		t.Fatalf("unexpected region: %s", opts.Region)
	}
	if opts.BaseEndpoint == nil || *opts.BaseEndpoint != "https://acct123.r2.cloudflarestorage.com" {
		t.Fatalf("unexpected base endpoint: %v", opts.BaseEndpoint)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && go test ./internal/r2upload/... -v`
Expected: FAIL — `undefined: UploadVideo`

- [ ] **Step 4: Write the implementation**

```go
// internal/r2upload/upload.go
package r2upload

import (
	"bytes"
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// PutObjectAPI e' il sottoinsieme del client S3 richiesto da UploadVideo,
// per permettere ai test di sostituirlo con un fake.
type PutObjectAPI interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

func NewR2Client(ctx context.Context, accountID, accessKeyID, secretAccessKey string) (*s3.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("auto"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	}), nil
}

// UploadVideo carica il video su R2 con key "<photoID>.mp4" (overwrite,
// naturalmente idempotente) e ritorna l'URL pubblico.
func UploadVideo(ctx context.Context, client PutObjectAPI, bucket, publicBaseURL, photoID string, videoBytes []byte) (string, error) {
	key := photoID + ".mp4"
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(videoBytes),
		ContentType: aws.String("video/mp4"),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s", publicBaseURL, key), nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && go test ./internal/r2upload/... -v`
Expected: 2 passed

- [ ] **Step 6: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add internal/r2upload/ go.mod go.sum
git commit -m "feat: upload extracted motion videos to Cloudflare R2 via S3-compatible API"
```

---

### Task 6: Git publish helper

**Files:**
- Create: `internal/gitpublish/publish.go`
- Test: `internal/gitpublish/publish_test.go`

**Interfaces:**
- Produces: `Publish(repoRoot, message string) (bool, error)` — esegue `git add -A`, poi `git commit -m message`; se non ci sono modifiche non fallisce (ritorna `false, nil`); esegue `git push` solo se il commit è stato creato (ritorna `true, nil`).
- Consumato da Task 7 (`internal/scanner`).

- [ ] **Step 1: Write the failing test**

Il test usa una repo git reale in una directory temporanea con un remote "origin" locale (bare repo), così il push viene esercitato per davvero senza toccare la rete. Il branch iniziale è forzato a `master` con `git init -b master`, per non dipendere dal default configurato globalmente sulla macchina (es. `main`).

```go
// internal/gitpublish/publish_test.go
package gitpublish

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v failed: %s: %v", args, out, err)
	}
}

func initRepoWithLocalRemote(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	remote := filepath.Join(tmp, "remote.git")
	runGit(t, tmp, "init", "--bare", remote)

	repo := filepath.Join(tmp, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	runGit(t, repo, "init", "-b", "master")
	runGit(t, repo, "config", "user.email", "test@example.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "remote", "add", "origin", remote)
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("init"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "init")
	runGit(t, repo, "push", "-u", "origin", "master")
	return repo
}

func TestPublishCommitsAndPushesWhenThereAreChanges(t *testing.T) {
	repo := initRepoWithLocalRemote(t)
	pagesDir := filepath.Join(repo, "site", "p")
	if err := os.MkdirAll(pagesDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pagesDir, "123.html"), []byte("<html></html>"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	pushed, err := Publish(repo, "publish motion photo 123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pushed {
		t.Fatal("expected pushed=true")
	}

	out, err := exec.Command("git", "-C", repo, "log", "-1", "--pretty=%s").Output()
	if err != nil {
		t.Fatalf("git log failed: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "publish motion photo 123" {
		t.Fatalf("got %q", got)
	}
}

func TestPublishReturnsFalseWhenNothingChanged(t *testing.T) {
	repo := initRepoWithLocalRemote(t)
	pushed, err := Publish(repo, "no-op")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pushed {
		t.Fatal("expected pushed=false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && go test ./internal/gitpublish/... -v`
Expected: FAIL — `undefined: Publish`

- [ ] **Step 3: Write the implementation**

```go
// internal/gitpublish/publish.go
package gitpublish

import (
	"fmt"
	"os/exec"
	"strings"
)

func Publish(repoRoot, message string) (bool, error) {
	addCmd := exec.Command("git", "add", "-A")
	addCmd.Dir = repoRoot
	if out, err := addCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git add failed: %s: %w", out, err)
	}

	commitCmd := exec.Command("git", "commit", "-m", message)
	commitCmd.Dir = repoRoot
	out, err := commitCmd.CombinedOutput()
	if err != nil {
		if strings.Contains(string(out), "nothing to commit") {
			return false, nil
		}
		return false, fmt.Errorf("git commit failed: %s: %w", out, err)
	}

	pushCmd := exec.Command("git", "push")
	pushCmd.Dir = repoRoot
	if out, err := pushCmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("git push failed: %s: %w", out, err)
	}

	return true, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && go test ./internal/gitpublish/... -v`
Expected: 2 passed

- [ ] **Step 5: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add internal/gitpublish/
git commit -m "feat: idempotent git commit+push helper for publishing viewer pages"
```

---

### Task 7: Scanner orchestrator + config + CLI entrypoint

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/scanner/scanner.go`
- Test: `internal/scanner/scanner_test.go`
- Create: `cmd/scanner/main.go`

**Interfaces:**
- Consumes: `motiondetect.DetectMotionPhoto` (Task 1), `flickrclient.Client`/`flickrclient.Photo` (Task 3), `sitegen.RenderPhotoPage`/`WritePhotoPage`/`PageData` (Task 4), `r2upload.UploadVideo`/`PutObjectAPI`/`NewR2Client` (Task 5), `gitpublish.Publish` (Task 6)
- Produces:
  - `config.Config` struct e `config.Load(siteRepoRoot string) (Config, error)`
  - `scanner.FlickrAPI` interfaccia (soddisfatta da `*flickrclient.Client`)
  - `scanner.ViewerBaseURL` variabile di package (default placeholder, sovrascritta da `cmd/scanner/main.go` via flag e in Task 9 col dominio reale)
  - `scanner.ProcessPhoto(client FlickrAPI, s3Client r2upload.PutObjectAPI, cfg config.Config, photo flickrclient.Photo, dryRun bool) (string, error)` — ritorna `"published"|"checked"|"lost"|"skipped"`
  - `scanner.Run(client FlickrAPI, s3Client r2upload.PutObjectAPI, cfg config.Config, limit int, dryRun bool) error` (`limit < 0` = illimitato)

- [ ] **Step 1: Write config.go**

Nessun test dedicato: è I/O di configurazione, verificato indirettamente dai test di `scanner` con `Config` costruito a mano.

```go
// internal/config/config.go
package config

import (
	"fmt"
	"os"
	"strings"
)

var requiredEnvVars = []string{
	"FLICKRGO_API_KEY",
	"FLICKRGO_API_SECRET",
	"FLICKR_OAUTH_TOKEN",
	"FLICKR_OAUTH_TOKEN_SECRET",
	"R2_ACCOUNT_ID",
	"R2_ACCESS_KEY_ID",
	"R2_SECRET_ACCESS_KEY",
	"R2_BUCKET",
	"R2_PUBLIC_BASE_URL",
}

type Config struct {
	FlickrAPIKey           string
	FlickrAPISecret        string
	FlickrOAuthToken       string
	FlickrOAuthTokenSecret string
	R2AccountID            string
	R2AccessKeyID          string
	R2SecretAccessKey      string
	R2Bucket               string
	R2PublicBaseURL        string
	SiteRepoRoot           string
}

func Load(siteRepoRoot string) (Config, error) {
	var missing []string
	for _, name := range requiredEnvVars {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf(
			"variabili d'ambiente mancanti: %s. Esegui bw-unlock o controlla Bitwarden 'Personal Secrets'.",
			strings.Join(missing, ", "),
		)
	}
	return Config{
		FlickrAPIKey:           os.Getenv("FLICKRGO_API_KEY"),
		FlickrAPISecret:        os.Getenv("FLICKRGO_API_SECRET"),
		FlickrOAuthToken:       os.Getenv("FLICKR_OAUTH_TOKEN"),
		FlickrOAuthTokenSecret: os.Getenv("FLICKR_OAUTH_TOKEN_SECRET"),
		R2AccountID:            os.Getenv("R2_ACCOUNT_ID"),
		R2AccessKeyID:          os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretAccessKey:      os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2Bucket:               os.Getenv("R2_BUCKET"),
		R2PublicBaseURL:        os.Getenv("R2_PUBLIC_BASE_URL"),
		SiteRepoRoot:           siteRepoRoot,
	}, nil
}
```

- [ ] **Step 2: Write the failing tests for scanner**

```go
// internal/scanner/scanner_test.go
package scanner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"motionphotos/internal/config"
	"motionphotos/internal/flickrclient"
	"motionphotos/internal/motiondetect"
	"motionphotos/internal/r2upload"
)

type fakeFlickrAPI struct {
	originalBytes       []byte
	originalURL         string
	description         string
	setDescriptionCalls []string
	addTagCalls         []string
}

func (f *fakeFlickrAPI) GetPhotosPage(page, perPage int) ([]flickrclient.Photo, error) {
	return nil, nil
}
func (f *fakeFlickrAPI) GetPhotoOriginalBytes(photoID string) ([]byte, error) { return f.originalBytes, nil }
func (f *fakeFlickrAPI) GetPhotoOriginalURL(photoID string) (string, error)  { return f.originalURL, nil }
func (f *fakeFlickrAPI) GetPhotoDescription(photoID string) (string, error) { return f.description, nil }
func (f *fakeFlickrAPI) SetDescription(photoID, description string) error {
	f.setDescriptionCalls = append(f.setDescriptionCalls, description)
	return nil
}
func (f *fakeFlickrAPI) AddTag(photoID, tag string) error {
	f.addTagCalls = append(f.addTagCalls, tag)
	return nil
}

func makeConfig(t *testing.T) config.Config {
	return config.Config{
		FlickrAPIKey: "k", FlickrAPISecret: "s",
		FlickrOAuthToken: "t", FlickrOAuthTokenSecret: "ts",
		R2AccountID: "acct", R2AccessKeyID: "rk", R2SecretAccessKey: "rs",
		R2Bucket: "bucket", R2PublicBaseURL: "https://videos.example.com",
		SiteRepoRoot: t.TempDir(),
	}
}

func writeTestTemplate(t *testing.T, repoRoot string) {
	t.Helper()
	siteDir := filepath.Join(repoRoot, "site")
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatalf("mkdir site: %v", err)
	}
	content := `<!doctype html><title>{{TITLE}}</title><img src="{{IMAGE_URL}}"><video src="{{VIDEO_URL}}"></video><a href="{{PHOTO_PAGE_URL}}"></a>`
	if err := os.WriteFile(filepath.Join(siteDir, "template.html"), []byte(content), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
}

func TestProcessPhotoSkipsIfAlreadyTagged(t *testing.T) {
	client := &fakeFlickrAPI{}
	photo := flickrclient.Photo{ID: "1", Title: "t", Tags: []string{"flickrmp:status=published"}}
	status, err := ProcessPhoto(client, nil, config.Config{}, photo, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "skipped" {
		t.Fatalf("got %s", status)
	}
	if len(client.setDescriptionCalls) != 0 {
		t.Fatal("expected no set_description calls")
	}
}

func TestProcessPhotoCheckedWhenNotMotion(t *testing.T) {
	orig := detectMotionPhotoFunc
	detectMotionPhotoFunc = func([]byte) motiondetect.Result {
		return motiondetect.Result{IsMotion: false, Reason: "checked"}
	}
	t.Cleanup(func() { detectMotionPhotoFunc = orig })

	client := &fakeFlickrAPI{originalBytes: []byte("JPEGDATA")}
	photo := flickrclient.Photo{ID: "2", Title: "t"}

	status, err := ProcessPhoto(client, nil, makeConfig(t), photo, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "checked" {
		t.Fatalf("got %s", status)
	}
	if len(client.addTagCalls) != 1 || client.addTagCalls[0] != "flickrmp:status=checked" {
		t.Fatalf("unexpected add tag calls: %v", client.addTagCalls)
	}
	if len(client.setDescriptionCalls) != 0 {
		t.Fatal("expected no set_description calls")
	}
}

func TestProcessPhotoLostWhenXMPButNoVideo(t *testing.T) {
	orig := detectMotionPhotoFunc
	detectMotionPhotoFunc = func([]byte) motiondetect.Result {
		return motiondetect.Result{IsMotion: false, Reason: "lost"}
	}
	t.Cleanup(func() { detectMotionPhotoFunc = orig })

	client := &fakeFlickrAPI{originalBytes: []byte("JPEGDATA")}
	photo := flickrclient.Photo{ID: "3", Title: "t"}

	status, err := ProcessPhoto(client, nil, makeConfig(t), photo, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "lost" {
		t.Fatalf("got %s", status)
	}
	if len(client.addTagCalls) != 1 || client.addTagCalls[0] != "flickrmp:status=lost" {
		t.Fatalf("unexpected add tag calls: %v", client.addTagCalls)
	}
}

func TestProcessPhotoPublishedFullFlow(t *testing.T) {
	origDetect := detectMotionPhotoFunc
	detectMotionPhotoFunc = func([]byte) motiondetect.Result {
		return motiondetect.Result{IsMotion: true, VideoBytes: []byte("VIDEO"), Reason: "published"}
	}
	t.Cleanup(func() { detectMotionPhotoFunc = origDetect })

	origUpload := uploadVideoFunc
	uploadVideoFunc = func(ctx context.Context, s3Client r2upload.PutObjectAPI, bucket, publicBaseURL, photoID string, videoBytes []byte) (string, error) {
		return "https://videos.example.com/4.mp4", nil
	}
	t.Cleanup(func() { uploadVideoFunc = origUpload })

	origPublish := publishFunc
	publishFunc = func(repoRoot, message string) (bool, error) { return true, nil }
	t.Cleanup(func() { publishFunc = origPublish })

	client := &fakeFlickrAPI{
		originalBytes: []byte("JPEGDATA"),
		originalURL:   "https://live.staticflickr.com/x/4_o.jpg",
		description:   "Descrizione originale",
	}
	photo := flickrclient.Photo{ID: "4", Title: "PXL_4.MP"}

	cfg := makeConfig(t)
	writeTestTemplate(t, cfg.SiteRepoRoot)

	status, err := ProcessPhoto(client, nil, cfg, photo, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "published" {
		t.Fatalf("got %s", status)
	}
	if len(client.setDescriptionCalls) != 1 {
		t.Fatalf("expected 1 set_description call, got %d", len(client.setDescriptionCalls))
	}
	desc := client.setDescriptionCalls[0]
	if !strings.Contains(desc, "Descrizione originale") {
		t.Fatalf("expected original description preserved, got %q", desc)
	}
	if !strings.Contains(desc, "Motion-Photo viewer: questa è una foto motion, guardala su") {
		t.Fatalf("expected viewer link sentence, got %q", desc)
	}
	if len(client.addTagCalls) != 1 || client.addTagCalls[0] != "flickrmp:status=published" {
		t.Fatalf("unexpected add tag calls: %v", client.addTagCalls)
	}
	if _, err := os.Stat(filepath.Join(cfg.SiteRepoRoot, "site", "p", "4.html")); err != nil {
		t.Fatalf("expected generated page to exist: %v", err)
	}
}

func TestProcessPhotoIdempotentSkipsDescriptionIfLinkAlreadyPresent(t *testing.T) {
	origDetect := detectMotionPhotoFunc
	detectMotionPhotoFunc = func([]byte) motiondetect.Result {
		return motiondetect.Result{IsMotion: true, VideoBytes: []byte("VIDEO"), Reason: "published"}
	}
	t.Cleanup(func() { detectMotionPhotoFunc = origDetect })

	origUpload := uploadVideoFunc
	uploadVideoFunc = func(ctx context.Context, s3Client r2upload.PutObjectAPI, bucket, publicBaseURL, photoID string, videoBytes []byte) (string, error) {
		return "https://videos.example.com/4.mp4", nil
	}
	t.Cleanup(func() { uploadVideoFunc = origUpload })

	origPublish := publishFunc
	publishFunc = func(repoRoot, message string) (bool, error) { return true, nil }
	t.Cleanup(func() { publishFunc = origPublish })

	origViewerBaseURL := ViewerBaseURL
	ViewerBaseURL = "https://motion.example.com"
	t.Cleanup(func() { ViewerBaseURL = origViewerBaseURL })

	client := &fakeFlickrAPI{
		originalBytes: []byte("JPEGDATA"),
		originalURL:   "https://live.staticflickr.com/x/4_o.jpg",
		description:   "Motion-Photo viewer: questa è una foto motion, guardala su https://motion.example.com/p/4.html",
	}
	photo := flickrclient.Photo{ID: "4", Title: "PXL_4.MP"}

	cfg := makeConfig(t)
	writeTestTemplate(t, cfg.SiteRepoRoot)

	status, err := ProcessPhoto(client, nil, cfg, photo, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "published" {
		t.Fatalf("got %s", status)
	}
	if len(client.setDescriptionCalls) != 0 {
		t.Fatal("expected no set_description call, link already present")
	}
}

func TestProcessPhotoDryRunMakesNoSideEffects(t *testing.T) {
	orig := detectMotionPhotoFunc
	detectMotionPhotoFunc = func([]byte) motiondetect.Result {
		return motiondetect.Result{IsMotion: true, VideoBytes: []byte("VIDEO"), Reason: "published"}
	}
	t.Cleanup(func() { detectMotionPhotoFunc = orig })

	client := &fakeFlickrAPI{originalBytes: []byte("JPEGDATA"), description: ""}
	photo := flickrclient.Photo{ID: "5", Title: "PXL_5.MP"}

	cfg := makeConfig(t)

	status, err := ProcessPhoto(client, nil, cfg, photo, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != "published" {
		t.Fatalf("got %s", status)
	}
	if len(client.setDescriptionCalls) != 0 {
		t.Fatal("expected no set_description call in dry-run")
	}
	if len(client.addTagCalls) != 0 {
		t.Fatal("expected no add_tag call in dry-run")
	}
	if _, err := os.Stat(filepath.Join(cfg.SiteRepoRoot, "site", "p", "5.html")); !os.IsNotExist(err) {
		t.Fatal("expected no generated page in dry-run")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && go test ./internal/scanner/... -v`
Expected: FAIL — `undefined: ProcessPhoto`

- [ ] **Step 4: Write the implementation**

```go
// internal/scanner/scanner.go
package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"motionphotos/internal/config"
	"motionphotos/internal/flickrclient"
	"motionphotos/internal/gitpublish"
	"motionphotos/internal/motiondetect"
	"motionphotos/internal/r2upload"
	"motionphotos/internal/sitegen"
)

const statusTagPrefix = "flickrmp:status="

// ViewerBaseURL è il dominio della pagina viewer pubblicata (GitHub Pages).
// Il placeholder va sostituito col dominio reale al Task 9.
var ViewerBaseURL = "https://motion.example.com"

// Variabili di package per dependency injection nei test.
var detectMotionPhotoFunc = motiondetect.DetectMotionPhoto
var uploadVideoFunc = r2upload.UploadVideo
var publishFunc = gitpublish.Publish

// FlickrAPI è il sottoinsieme di flickrclient.Client richiesto dallo scanner.
type FlickrAPI interface {
	GetPhotosPage(page, perPage int) ([]flickrclient.Photo, error)
	GetPhotoOriginalBytes(photoID string) ([]byte, error)
	GetPhotoOriginalURL(photoID string) (string, error)
	GetPhotoDescription(photoID string) (string, error)
	SetDescription(photoID, description string) error
	AddTag(photoID, tag string) error
}

func alreadyTagged(tags []string) bool {
	for _, t := range tags {
		if strings.HasPrefix(t, statusTagPrefix) {
			return true
		}
	}
	return false
}

func ProcessPhoto(client FlickrAPI, s3Client r2upload.PutObjectAPI, cfg config.Config, photo flickrclient.Photo, dryRun bool) (string, error) {
	if alreadyTagged(photo.Tags) {
		return "skipped", nil
	}

	jpegBytes, err := client.GetPhotoOriginalBytes(photo.ID)
	if err != nil {
		return "", err
	}
	result := detectMotionPhotoFunc(jpegBytes)

	if !result.IsMotion {
		if !dryRun {
			if err := client.AddTag(photo.ID, statusTagPrefix+result.Reason); err != nil {
				return "", err
			}
		}
		return result.Reason, nil
	}

	viewerURL := fmt.Sprintf("%s/p/%s.html", ViewerBaseURL, photo.ID)

	if dryRun {
		return "published", nil
	}

	videoURL, err := uploadVideoFunc(context.Background(), s3Client, cfg.R2Bucket, cfg.R2PublicBaseURL, photo.ID, result.VideoBytes)
	if err != nil {
		return "", err
	}

	templateBytes, err := os.ReadFile(filepath.Join(cfg.SiteRepoRoot, "site", "template.html"))
	if err != nil {
		return "", err
	}

	imageURL, err := client.GetPhotoOriginalURL(photo.ID)
	if err != nil {
		return "", err
	}

	html := sitegen.RenderPhotoPage(string(templateBytes), sitegen.PageData{
		PhotoID:      photo.ID,
		Title:        photo.Title,
		ImageURL:     imageURL,
		VideoURL:     videoURL,
		PhotoPageURL: fmt.Sprintf("https://www.flickr.com/photos/lorello/%s/", photo.ID),
	})
	if _, err := sitegen.WritePhotoPage(filepath.Join(cfg.SiteRepoRoot, "site"), photo.ID, html); err != nil {
		return "", err
	}
	if _, err := publishFunc(cfg.SiteRepoRoot, fmt.Sprintf("publish motion photo %s", photo.ID)); err != nil {
		return "", err
	}

	currentDescription, err := client.GetPhotoDescription(photo.ID)
	if err != nil {
		return "", err
	}
	if !strings.Contains(currentDescription, viewerURL) {
		sentence := "Motion-Photo viewer: questa è una foto motion, guardala su " + viewerURL
		newDescription := strings.TrimSpace(currentDescription + "\n\n" + sentence)
		if err := client.SetDescription(photo.ID, newDescription); err != nil {
			return "", err
		}
	}

	if err := client.AddTag(photo.ID, statusTagPrefix+"published"); err != nil {
		return "", err
	}
	return "published", nil
}

// Run scansiona tutte le pagine di foto pubbliche fino a limit (limit < 0 =
// illimitato), processando ogni foto e stampando lo stato risultante.
func Run(client FlickrAPI, s3Client r2upload.PutObjectAPI, cfg config.Config, limit int, dryRun bool) error {
	processed := 0
	page := 1
	for limit < 0 || processed < limit {
		photos, err := client.GetPhotosPage(page, 100)
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
			status, err := ProcessPhoto(client, s3Client, cfg, photo, dryRun)
			if err != nil {
				return fmt.Errorf("photo %s: %w", photo.ID, err)
			}
			fmt.Printf("%s\t%s\t%s\n", photo.ID, status, photo.Title)
			if status != "skipped" {
				processed++
			}
		}
		page++
	}
	return nil
}
```

```go
// cmd/scanner/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"motionphotos/internal/config"
	"motionphotos/internal/flickrclient"
	"motionphotos/internal/r2upload"
	"motionphotos/internal/scanner"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "Rileva senza modificare Flickr/R2/git")
	limit := flag.Int("limit", -1, "Numero massimo di foto da processare (-1 = illimitato)")
	repoRoot := flag.String("repo-root", ".", "Root del repository con site/template.html")
	viewerBaseURL := flag.String("viewer-base-url", "https://motion.example.com", "Dominio della pagina viewer pubblicata (GitHub Pages)")
	flag.Parse()

	scanner.ViewerBaseURL = *viewerBaseURL

	cfg, err := config.Load(*repoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	client := flickrclient.NewClient(cfg.FlickrAPIKey, cfg.FlickrAPISecret, cfg.FlickrOAuthToken, cfg.FlickrOAuthTokenSecret)

	ctx := context.Background()
	s3Client, err := r2upload.NewR2Client(ctx, cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretAccessKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := scanner.Run(client, s3Client, cfg, *limit, *dryRun); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd /home/lorello/src/flickr/flickr-motion-photos && go test ./internal/scanner/... -v && go build ./...`
Expected: 6 test passed, build OK

- [ ] **Step 6: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add internal/config/ internal/scanner/ cmd/scanner/
git commit -m "feat: scanner orchestrator + CLI entrypoint wiring detection, upload, publish, tagging"
```

---

### Task 8: OAuth authorization one-off CLI

**Files:**
- Create: `cmd/authorize/main.go`

**Interfaces:**
- Consumes: `flickroauth.GetRequestToken`, `flickroauth.BuildAuthorizeURL`, `flickroauth.GetAccessToken` (Task 2)
- Produce: stampa a schermo `FLICKR_OAUTH_TOKEN` e `FLICKR_OAUTH_TOKEN_SECRET` da salvare manualmente in Bitwarden.

Questo script non necessita di test automatici: è un flusso interattivo one-off (già eseguito manualmente e validato con successo durante lo spike di design, in una sessione precedente, contro l'account reale `lorello` — con l'implementazione Python, logica identica). L'implementazione replica esattamente la sequenza già provata.

- [ ] **Step 1: Scrivi il comando**

```go
// cmd/authorize/main.go
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"motionphotos/internal/flickroauth"
)

func main() {
	consumerKey := os.Getenv("FLICKRGO_API_KEY")
	consumerSecret := os.Getenv("FLICKRGO_API_SECRET")
	if consumerKey == "" || consumerSecret == "" {
		fmt.Fprintln(os.Stderr, "Servono FLICKRGO_API_KEY e FLICKRGO_API_SECRET nell'ambiente (bw-unlock).")
		os.Exit(1)
	}

	request, err := flickroauth.GetRequestToken(consumerKey, consumerSecret)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	requestToken := request["oauth_token"]
	requestTokenSecret := request["oauth_token_secret"]

	fmt.Println("Apri questo URL, autorizza l'app, e incolla qui il verifier mostrato da Flickr:")
	fmt.Println(flickroauth.BuildAuthorizeURL(requestToken, "write"))
	fmt.Print("Verifier: ")

	reader := bufio.NewReader(os.Stdin)
	verifierLine, _ := reader.ReadString('\n')
	verifier := strings.TrimSpace(verifierLine)

	access, err := flickroauth.GetAccessToken(consumerKey, consumerSecret, requestToken, requestTokenSecret, verifier)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("Autenticato come:", access["username"])
	fmt.Println()
	fmt.Println("Salva questi due valori in Bitwarden 'Personal Secrets' come campi custom:")
	fmt.Printf("  FLICKR_OAUTH_TOKEN = %s\n", access["oauth_token"])
	fmt.Printf("  FLICKR_OAUTH_TOKEN_SECRET = %s\n", access["oauth_token_secret"])
	fmt.Println()
	fmt.Println("Poi esegui bw-unlock in una nuova shell per caricarli.")
}
```

- [ ] **Step 2: Build**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
go build ./cmd/authorize
```

Expected: build OK, nessun errore.

- [ ] **Step 3: Esegui manualmente e salva le credenziali**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
go run ./cmd/authorize
```

Segui il flusso (già validato nello spike): apri l'URL, autorizza, incolla il verifier. Copia i due valori stampati in Bitwarden desktop/web, item "Personal Secrets", come nuovi campi custom `FLICKR_OAUTH_TOKEN ...` e `FLICKR_OAUTH_TOKEN_SECRET ...`. Esegui `bw-unlock` in una shell per ricaricare i secrets.

- [ ] **Step 4: Commit**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add cmd/authorize/
git commit -m "feat: one-off OAuth authorization CLI"
```

---

### Task 9: Infrastruttura esterna (GitHub Pages + Cloudflare R2) e test end-to-end

**Files:**
- Create: `.gitignore` (esclude i binari compilati)
- Modify: `cmd/scanner/main.go` (sostituire il default del flag `-viewer-base-url` col dominio reale di GitHub Pages)
- Nessun file di test: infrastruttura esterna, verificata con uno smoke test manuale reale seguito dal run end-to-end previsto dalla spec.

**⚠️ Side effect esterni:** questo task crea una repo GitHub pubblica, effettua push reali, e crea risorse cloud reali (bucket R2, API token) con credenziali che finiscono in Bitwarden. Prima di eseguirlo, conferma con l'utente nome repo/bucket e che sia ok procedere — non è un task da eseguire autonomamente senza conferma esplicita (side effect esterni al repo/worktree, azioni di rete verso account reali).

- [ ] **Step 1: Crea il repository GitHub per la pagina viewer**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
gh repo create lorello/flickr-motion-photos --public --source=. --remote=origin --push
```

- [ ] **Step 2: Abilita GitHub Pages sulla cartella `site/` del branch main**

```bash
gh api repos/lorello/flickr-motion-photos/pages -X POST -f "source[branch]=main" -f "source[path]=/site" 2>&1 || \
gh api repos/lorello/flickr-motion-photos/pages -X PUT -f "source[branch]=main" -f "source[path]=/site"
```

Verifica che la pagina sia raggiungibile (potrebbe richiedere 1-2 minuti dopo l'attivazione):

```bash
curl -s -o /dev/null -w "%{http_code}\n" https://lorello.github.io/flickr-motion-photos/
```

Expected: `200` (anche una index vuota va bene per ora, basta che Pages sia attivo)

- [ ] **Step 2b: Aggiorna il default di `-viewer-base-url` in `cmd/scanner/main.go`**

```go
// cmd/scanner/main.go
viewerBaseURL := flag.String("viewer-base-url", "https://lorello.github.io/flickr-motion-photos", "Dominio della pagina viewer pubblicata (GitHub Pages)")
```

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add cmd/scanner/main.go
git commit -m "chore: point default viewer-base-url to real GitHub Pages domain"
```

- [ ] **Step 3: Crea il bucket Cloudflare R2 e le credenziali**

Manuale via dashboard Cloudflare (dash.cloudflare.com → R2):
1. Crea bucket `flickr-motion-photos-videos`
2. Abilita "Public access" con dominio pubblico (o custom domain) — annota l'URL pubblico base (es. `https://pub-xxxx.r2.dev` o il tuo dominio custom)
3. Configura CORS sul bucket per permettere `GET` da qualsiasi origine (necessario per il tag `<video>` cross-origin):

```json
[
  {
    "AllowedOrigins": ["*"],
    "AllowedMethods": ["GET"],
    "AllowedHeaders": ["*"]
  }
]
```

4. Crea un R2 API Token (dashboard → R2 → Manage API Tokens) con permessi "Object Read & Write" limitati al bucket `flickr-motion-photos-videos`. Annota `Access Key ID` e `Secret Access Key`, e l'Account ID (visibile nell'URL della dashboard o nella pagina R2 overview).

- [ ] **Step 4: Salva le credenziali R2 in Bitwarden**

Aggiungi in Bitwarden "Personal Secrets" i campi custom:
- `R2_ACCOUNT_ID`
- `R2_ACCESS_KEY_ID`
- `R2_SECRET_ACCESS_KEY`
- `R2_BUCKET flickr-motion-photos-videos`
- `R2_PUBLIC_BASE_URL` (l'URL pubblico annotato al passo 3.2)

Esegui `bw-unlock` per caricarli nella shell corrente.

- [ ] **Step 5: Crea .gitignore per i binari compilati**

```bash
cat > /home/lorello/src/flickr/flickr-motion-photos/.gitignore <<'EOF'
/bin/
EOF
```

- [ ] **Step 6: Smoke test upload R2**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
mkdir -p bin
go build -o bin/scanner ./cmd/scanner
go build -o bin/authorize ./cmd/authorize
go run -exec "" - <<'EOF'
package main

import (
	"context"
	"fmt"
	"os"

	"motionphotos/internal/r2upload"
)

func main() {
	ctx := context.Background()
	client, err := r2upload.NewR2Client(ctx, os.Getenv("R2_ACCOUNT_ID"), os.Getenv("R2_ACCESS_KEY_ID"), os.Getenv("R2_SECRET_ACCESS_KEY"))
	if err != nil {
		panic(err)
	}
	url, err := r2upload.UploadVideo(ctx, client, os.Getenv("R2_BUCKET"), os.Getenv("R2_PUBLIC_BASE_URL"), "smoketest", []byte("not a real video, just bytes"))
	if err != nil {
		panic(err)
	}
	fmt.Println(url)
}
EOF
curl -s -o /dev/null -w "%{http_code}\n" "${R2_PUBLIC_BASE_URL}/smoketest.mp4"
```

Expected: ultimo comando stampa `200`

(Nota: se `go run -exec "" -` con here-doc non è supportato dalla toolchain locale, scrivi lo snippet in un file temporaneo `/tmp/smoketest.go` ed esegui `go run /tmp/smoketest.go` — è solo uno smoke test manuale, non entra nel repo.)

- [ ] **Step 7: Esegui `authorize` (Task 8) se non ancora fatto, poi test end-to-end reale**

Segui il flusso descritto dalla spec (`docs/superpowers/specs/2026-09-07-motion-photo-viewer-design.md`, sezione Testing):

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
bw-unlock   # carica tutti i secrets aggiornati
go run ./cmd/scanner --dry-run --limit 5
```

Verifica nell'output che le foto motion note (es. `55391822566`) risultino `published` in dry-run senza errori.

```bash
go run ./cmd/scanner --limit 5
```

Verifica manualmente:
- la pagina `https://lorello.github.io/flickr-motion-photos/p/55391822566.html` mostra foto+video in loop
- la descrizione della foto su Flickr contiene la frase `Motion-Photo viewer: questa è una foto motion, guardala su ...`
- la foto ha il tag `flickrmp:status=published`

Rerun per verificare idempotenza:

```bash
go run ./cmd/scanner --limit 5
```

Expected: tutte le foto del batch precedente risultano `skipped` (già taggate), nessun duplicato in descrizione.

- [ ] **Step 8: Commit finale di chiusura task**

```bash
cd /home/lorello/src/flickr/flickr-motion-photos
git add -A
git commit -m "chore: wire real GitHub Pages + R2 infra, validate end-to-end" --allow-empty
```

---

## Self-Review

**Copertura spec:**
- Detection XMP + payload MP4, offset `ftyp-4` → Task 1 ✅
- OAuth 1.0a locale, unico per CLI → Task 2, 8 ✅
- Download originale via `getInfo`, sempre autenticato → Task 3 ✅
- Machine tag `flickrmp:status=*` come stato, nessun file locale → Task 3 (tags), Task 7 (`alreadyTagged`) ✅
- `privacy_filter=1` per restare sulle sole foto pubbliche → Task 3 (`GetPhotosPage`), coperto da test dedicato ✅
- Pagina viewer stile dark Flickr-like → Task 4 ✅
- Upload video R2 → Task 5 ✅
- Publish GitHub Pages → Task 6 ✅
- Frase descrizione `"Motion-Photo viewer: questa è una foto motion, guardala su <url>"` → Task 7, coperta da test (full flow + idempotenza) ✅
- Ordine idempotente (tag per ultimo, description-check, video overwrite-safe) → Task 7 ✅
- `--dry-run` / `--limit` → Task 7 ✅
- Secrets via Bitwarden → Task 8, 9 (istruzioni esplicite) ✅
- Testing: unit test automatici per package + end-to-end manuale (dry-run → run reale → rerun idempotente) → Task 1-8 (unit), Task 9 (e2e) ✅
- Fase 2 (multi-utente, permessi Flickr) → esplicitamente fuori scope, non pianificata qui, coerente con la spec ✅

Nessun gap rilevato.

**Placeholder scan:** un solo placeholder intenzionale e dichiarato — il default del flag `-viewer-base-url` in `cmd/scanner/main.go` (Task 7), esplicitamente segnalato come da sostituire al Task 9 Step 2b (non è un "implementare dopo" generico: il valore reale e il comando esatto per sostituirlo sono nel piano).

**Type consistency:** verificato — `motiondetect.Result.Reason` (Task 1) coincide esattamente con i valori usati con `statusTagPrefix` (Task 7) e nella spec. `config.Config` (Task 7) usa esattamente i nomi di campo consumati da `flickrclient.NewClient` e `r2upload.NewR2Client`/`UploadVideo` (Task 3, 5). `scanner.FlickrAPI` (Task 7) elenca esattamente i metodi esportati da `*flickrclient.Client` (Task 3) — nessuna discrepanza di firma. `scanner.ProcessPhoto`/`Run` usano `r2upload.PutObjectAPI` (Task 5) come tipo del parametro `s3Client`, coerente col tipo ritornato da `r2upload.NewR2Client` (`*s3.Client`, che soddisfa l'interfaccia per struttura). Nessuna discrepanza residua.

**Decisioni prese rispetto al design Python originale (stessa architettura, adattamenti Go-specifici):**
- `git init -b master` esplicito nel test di Task 6, per non dipendere dal branch di default configurato globalmente sulla macchina (l'implementazione Python porterebbe lo stesso rischio, non notato nello spike originale).
- `limit int` con convenzione `< 0` = illimitato, al posto di `Optional[int] = None` (Go non ha optional).
- Dependency injection tramite variabili di package (`var xFunc = pkg.RealFunc`) per singole dipendenze a effetto, e interfacce minimali lato consumer (`scanner.FlickrAPI`, `r2upload.PutObjectAPI`) per collaboratori multi-metodo — pattern idiomatico Go equivalente al monkeypatching usato nei test Python.
