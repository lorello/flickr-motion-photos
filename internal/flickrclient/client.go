package flickrclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"motionphotos/internal/flickroauth"
)

// licenseNames/licenseURLs map Flickr's license ID (from getInfo's "license"
// field) to a human name and its canonical URL. This table is Flickr's own
// stable, official enumeration (flickr.photos.licenses.getInfo) — hardcoded
// rather than fetched per photo since it's global, not per-photo, data.
var licenseNames = map[string]string{
	"0": "All Rights Reserved", "1": "CC BY-NC-SA 2.0", "2": "CC BY-NC 2.0",
	"3": "CC BY-NC-ND 2.0", "4": "CC BY 2.0", "5": "CC BY-SA 2.0", "6": "CC BY-ND 2.0",
	"7": "No known copyright restrictions", "8": "United States Government Work",
	"9": "Public Domain Dedication (CC0)", "10": "Public Domain Mark",
	"11": "CC BY 4.0", "12": "CC BY-SA 4.0", "13": "CC BY-ND 4.0",
	"14": "CC BY-NC 4.0", "15": "CC BY-NC-SA 4.0", "16": "CC BY-NC-ND 4.0",
}

var licenseURLs = map[string]string{
	"0":  "https://www.flickrhelp.com/hc/en-us/articles/10710266545556-Using-Flickr-images-shared-by-other-members",
	"1":  "https://creativecommons.org/licenses/by-nc-sa/2.0/",
	"2":  "https://creativecommons.org/licenses/by-nc/2.0/",
	"3":  "https://creativecommons.org/licenses/by-nc-nd/2.0/",
	"4":  "https://creativecommons.org/licenses/by/2.0/",
	"5":  "https://creativecommons.org/licenses/by-sa/2.0/",
	"6":  "https://creativecommons.org/licenses/by-nd/2.0/",
	"7":  "https://www.flickr.com/commons/usage/",
	"8":  "https://www.usa.gov/government-copyright",
	"9":  "https://creativecommons.org/publicdomain/zero/1.0/",
	"10": "https://creativecommons.org/publicdomain/mark/1.0/",
	"11": "https://creativecommons.org/licenses/by/4.0/",
	"12": "https://creativecommons.org/licenses/by-sa/4.0/",
	"13": "https://creativecommons.org/licenses/by-nd/4.0/",
	"14": "https://creativecommons.org/licenses/by-nc/4.0/",
	"15": "https://creativecommons.org/licenses/by-nc-sa/4.0/",
	"16": "https://creativecommons.org/licenses/by-nc-nd/4.0/",
}

const apiURL = "https://api.flickr.com/services/rest"

// ErrOriginalNotAvailable segnala che Flickr non espone originalsecret/
// originalformat per questa foto — richiede un client autenticato come
// owner (OAuth), a prescindere dal fatto che la foto sia pubblica.
var ErrOriginalNotAvailable = errors.New("original not available: owner-authenticated OAuth required")

// signedGetFunc, unsignedGetFunc e httpGetFunc permettono ai test di
// sostituire il livello HTTP.
var signedGetFunc = flickroauth.SignedGet
var httpGetFunc = http.Get

// downloadOriginalFunc scarica i byte dell'originale da live.staticflickr.com.
// Implementazione di default: shell out a curl invece del client HTTP nativo
// di Go. Verificato in sessione: il CDN di Flickr (CloudFront/WAF davanti a
// live.staticflickr.com) risponde 502 "Error from cloudfront" a ripetizione
// alle richieste fatte con lo stack TLS standard di Go (stesso URL, stessi
// header, sia HTTP/1.1 che HTTP/2 — provato), ma accetta senza problemi la
// stessa identica richiesta fatta da curl. Le chiamate JSON verso
// api.flickr.com non sono affette, restano su httpGetFunc/unsignedGetFunc.
// Iniettabile nei test.
var downloadOriginalFunc = curlDownload

// downloadThrottle: pausa proattiva prima di ogni download, per non
// bombardare il CDN durante una scansione di migliaia di foto. Verificato
// in sessione che non basta da solo (vedi cdnCooldown sotto): fa comunque
// da ritmo base tra i download quando il CDN non è in cooldown.
const downloadThrottle = 1500 * time.Millisecond

// sleepFunc è iniettabile nei test per non rallentarli con gli sleep veri.
var sleepFunc = time.Sleep

// cdnCooldown implementa un circuit breaker globale (per processo, non per
// singola foto) sui 429 dal CDN. Verificato in sessione: un retry-con-backoff
// per-singola-foto (1s/2s/4s/8s) non risolve — 4 foto diverse, spaziate
// manualmente fino a 8s, hanno dato 429 su *tutti* i tentativi, segno che il
// nostro stesso traffico sostenuto durante uno scan di migliaia di foto ha
// fatto scattare un blocco più lungo di una singola foto, e continuare a
// ritentare aggressivamente durante quel blocco probabilmente lo rinnova
// invece di farlo scadere. Il fix è quindi un cooldown condiviso tra TUTTE
// le foto dello scan: un 429 blocca ogni download successivo fino a
// blockedUntil (backoff esponenziale, azzerato al primo successo), invece
// che ogni foto ritentando per conto suo.
type cdnCooldown struct {
	mu           sync.Mutex
	blockedUntil time.Time
	nextBackoff  time.Duration
}

var downloadCooldown = &cdnCooldown{nextBackoff: time.Minute}

func (c *cdnCooldown) waitIfBlocked() {
	c.mu.Lock()
	until := c.blockedUntil
	c.mu.Unlock()
	if wait := time.Until(until); wait > 0 {
		sleepFunc(wait)
	}
}

func (c *cdnCooldown) recordRateLimited() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blockedUntil = time.Now().Add(c.nextBackoff)
	if c.nextBackoff < 30*time.Minute {
		c.nextBackoff *= 2
	}
}

func (c *cdnCooldown) recordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextBackoff = time.Minute
}

func curlDownload(rawURL string) (body []byte, statusCode int, err error) {
	downloadCooldown.waitIfBlocked()
	sleepFunc(downloadThrottle)
	body, statusCode, err = curlDownloadOnce(rawURL)
	if err == nil {
		if statusCode == http.StatusTooManyRequests {
			downloadCooldown.recordRateLimited()
		} else {
			downloadCooldown.recordSuccess()
		}
	}
	return body, statusCode, err
}

func curlDownloadOnce(rawURL string) (body []byte, statusCode int, err error) {
	cmd := exec.Command("curl", "-sS", "-w", "\n%{http_code}", rawURL)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, 0, fmt.Errorf("curl failed downloading %s: %w (stderr: %s)", rawURL, err, stderr.String())
	}

	output := stdout.Bytes()
	idx := bytes.LastIndexByte(output, '\n')
	if idx < 0 {
		return nil, 0, fmt.Errorf("unexpected curl output for %s: no status code marker", rawURL)
	}
	respBody := output[:idx]
	statusStr := strings.TrimSpace(string(output[idx+1:]))
	status, convErr := strconv.Atoi(statusStr)
	if convErr != nil {
		return nil, 0, fmt.Errorf("unexpected curl status output %q for %s: %w", statusStr, rawURL, convErr)
	}
	return respBody, status, nil
}

// unsignedGetFunc esegue una GET pubblica con solo api_key, senza firma
// OAuth. Usato per i metodi read-only su dati pubblici quando non e'
// disponibile un access token (nessuna auth ancora completata).
var unsignedGetFunc = func(rawURL string, params map[string]string) (string, error) {
	query := url.Values{}
	for k, v := range params {
		query.Set(k, v)
	}
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

type APIError struct {
	Message string
}

func (e *APIError) Error() string { return e.Message }

type Photo struct {
	ID    string
	Title string
	Tags  []string
}

// Client parla con l'API REST di Flickr. Se AccessToken/AccessTokenSecret
// sono vuoti, le chiamate vengono firmate solo con la consumer key (api_key),
// senza OAuth: funziona per i metodi read-only su dati pubblici (getInfo,
// people.getPhotos), ma alcuni campi (es. originalsecret) restano nascosti
// e le chiamate di scrittura falliscono — serve un access token per quelle.
type Client struct {
	ConsumerKey       string
	ConsumerSecret    string
	AccessToken       string
	AccessTokenSecret string
}

func NewClient(consumerKey, consumerSecret, accessToken, accessTokenSecret string) *Client {
	return &Client{consumerKey, consumerSecret, accessToken, accessTokenSecret}
}

// Authenticated indica se il client ha un access token OAuth (quindi puo'
// fare chiamate di scrittura e vedere campi privati come originalsecret).
func (c *Client) Authenticated() bool {
	return c.AccessToken != "" && c.AccessTokenSecret != ""
}

func (c *Client) Call(method string, params map[string]string) (map[string]interface{}, error) {
	var body string
	var err error

	if c.Authenticated() {
		full := flickroauth.BuildOAuthParams(c.ConsumerKey, c.AccessToken)
		full["method"] = method
		full["format"] = "json"
		full["nojsoncallback"] = "1"
		for k, v := range params {
			full[k] = v
		}
		body, err = signedGetFunc(apiURL, full, c.ConsumerKey, c.ConsumerSecret, c.AccessToken, c.AccessTokenSecret)
	} else {
		full := map[string]string{
			"method":         method,
			"api_key":        c.ConsumerKey,
			"format":         "json",
			"nojsoncallback": "1",
		}
		for k, v := range params {
			full[k] = v
		}
		body, err = unsignedGetFunc(apiURL, full)
	}
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

// PhotoDetails carries the extra display metadata the viewer page uses,
// all sourced from a single getInfo call (no extra API cost per photo).
type PhotoDetails struct {
	OwnerName      string
	OwnerAvatarURL string
	DateTaken      string
	DateUploaded   string
	Tags           []string
	Views          string
	Comments       string
	LicenseName    string
	LicenseURL     string
}

// GetPhotoDetails returns owner/avatar/date/tags for the viewer page.
// Internal status tags (flickrmp:*) are filtered out — they're not
// meant for display.
func (c *Client) GetPhotoDetails(photoID string) (PhotoDetails, error) {
	info, err := c.getPhotoInfo(photoID)
	if err != nil {
		return PhotoDetails{}, err
	}

	owner, _ := info["owner"].(map[string]interface{})
	username, _ := owner["username"].(string)
	nsid, _ := owner["nsid"].(string)
	iconServer, _ := owner["iconserver"].(string)
	iconFarm, _ := owner["iconfarm"].(float64)

	avatarURL := "https://www.flickr.com/images/buddyicon.gif"
	if iconServer != "" && iconServer != "0" {
		avatarURL = fmt.Sprintf("https://farm%d.staticflickr.com/%s/buddyicons/%s.jpg", int(iconFarm), iconServer, nsid)
	}

	dates, _ := info["dates"].(map[string]interface{})
	dateTaken, _ := dates["taken"].(string)

	tagsObj, _ := info["tags"].(map[string]interface{})
	rawTagList, _ := tagsObj["tag"].([]interface{})
	tags := make([]string, 0, len(rawTagList))
	for _, item := range rawTagList {
		t, _ := item.(map[string]interface{})
		content, _ := t["_content"].(string)
		if content == "" || strings.HasPrefix(content, "flickrmp:") {
			continue // internal bookkeeping namespace, not for display
		}
		tags = append(tags, content)
	}

	views, _ := info["views"].(string)

	commentsObj, _ := info["comments"].(map[string]interface{})
	comments, _ := commentsObj["_content"].(string)

	var dateUploaded string
	if rawUploaded, _ := info["dateuploaded"].(string); rawUploaded != "" {
		if secs, err := strconv.ParseInt(rawUploaded, 10, 64); err == nil {
			dateUploaded = time.Unix(secs, 0).UTC().Format("2006-01-02")
		}
	}

	licenseID, _ := info["license"].(string)

	return PhotoDetails{
		OwnerName: username, OwnerAvatarURL: avatarURL,
		DateTaken: dateTaken, DateUploaded: dateUploaded,
		Tags: tags, Views: views, Comments: comments,
		LicenseName: licenseNames[licenseID], LicenseURL: licenseURLs[licenseID],
	}, nil
}

// GetPhotoFavoritesCount returns how many users have favorited the photo,
// via a single flickr.photos.getFavorites call with per_page=1 (we only
// need the "total" count, not the list of favoriters).
func (c *Client) GetPhotoFavoritesCount(photoID string) (string, error) {
	result, err := c.Call("flickr.photos.getFavorites", map[string]string{"photo_id": photoID, "per_page": "1"})
	if err != nil {
		return "", err
	}
	photo, _ := result["photo"].(map[string]interface{})
	total, ok := photo["total"].(float64)
	if !ok {
		return "0", nil
	}
	return strconv.Itoa(int(total)), nil
}

// GetPhotoGeo returns the photo's latitude/longitude if it has one AND its
// location is public. hasGeo is false (no error) both when the photo isn't
// geotagged and when its location is private/friends/family-only — Flickr
// lets an owner mark a public photo's *location* private independently of
// the photo itself (geo.getLocation happily returns coordinates to the
// authenticated owner regardless), so skipping the geo.getPerms check
// would leak a location the owner deliberately didn't make public onto
// this page. Verified live: a real photo here had ispublic=0, isfamily=1.
func (c *Client) GetPhotoGeo(photoID string) (lat, lon string, hasGeo bool, err error) {
	permsResult, permsErr := c.Call("flickr.photos.geo.getPerms", map[string]string{"photo_id": photoID})
	if permsErr != nil {
		var apiErr *APIError
		if errors.As(permsErr, &apiErr) {
			return "", "", false, nil // can't confirm the location is public -> don't show it
		}
		return "", "", false, permsErr
	}
	perms, _ := permsResult["perms"].(map[string]interface{})
	isPublic, _ := perms["ispublic"].(float64)
	if isPublic != 1 {
		return "", "", false, nil
	}

	result, callErr := c.Call("flickr.photos.geo.getLocation", map[string]string{"photo_id": photoID})
	if callErr != nil {
		var apiErr *APIError
		if errors.As(callErr, &apiErr) {
			return "", "", false, nil
		}
		return "", "", false, callErr
	}
	photo, _ := result["photo"].(map[string]interface{})
	location, _ := photo["location"].(map[string]interface{})
	lat, _ = location["latitude"].(string)
	lon, _ = location["longitude"].(string)
	if lat == "" || lon == "" {
		return "", "", false, nil
	}
	return lat, lon, true, nil
}

// GetPhotoGroups returns the names of the groups (pools) this photo has
// been added to, if any.
func (c *Client) GetPhotoGroups(photoID string) ([]string, error) {
	result, err := c.Call("flickr.photos.getAllContexts", map[string]string{"photo_id": photoID})
	if err != nil {
		return nil, err
	}
	rawPools, _ := result["pool"].([]interface{})
	groups := make([]string, 0, len(rawPools))
	for _, item := range rawPools {
		p, _ := item.(map[string]interface{})
		title, _ := p["title"].(string)
		if title != "" {
			groups = append(groups, title)
		}
	}
	return groups, nil
}

// GetPhotoPeople returns the usernames of people tagged in this photo, if
// any.
func (c *Client) GetPhotoPeople(photoID string) ([]string, error) {
	result, err := c.Call("flickr.photos.people.getList", map[string]string{"photo_id": photoID})
	if err != nil {
		return nil, err
	}
	peopleObj, _ := result["people"].(map[string]interface{})
	rawPeople, _ := peopleObj["person"].([]interface{})
	people := make([]string, 0, len(rawPeople))
	for _, item := range rawPeople {
		p, _ := item.(map[string]interface{})
		username, _ := p["username"].(string)
		if username != "" {
			people = append(people, username)
		}
	}
	return people, nil
}

// PhotoExif carries camera/exposure info for the viewer page's "additional
// information" panel. Requires a separate flickr.photos.getExif call (not
// included in getInfo) — anonymous calls are rejected ("Permission
// denied"), so this only works when the client is OAuth-authenticated.
type PhotoExif struct {
	Camera       string
	ExposureTime string
	FNumber      string
	ISO          string
	FocalLength  string
}

func (c *Client) GetPhotoExif(photoID string) (PhotoExif, error) {
	result, err := c.Call("flickr.photos.getExif", map[string]string{"photo_id": photoID})
	if err != nil {
		return PhotoExif{}, err
	}
	photo, _ := result["photo"].(map[string]interface{})
	camera, _ := photo["camera"].(string)

	raw := map[string]string{}
	exifList, _ := photo["exif"].([]interface{})
	for _, item := range exifList {
		e, _ := item.(map[string]interface{})
		tag, _ := e["tag"].(string)
		rawObj, _ := e["raw"].(map[string]interface{})
		content, _ := rawObj["_content"].(string)
		raw[tag] = content
	}

	fNumber := raw["FNumber"]
	if fNumber != "" {
		fNumber = "f/" + fNumber
	}
	iso := raw["ISO"]
	if iso != "" {
		iso = "ISO " + iso
	}

	return PhotoExif{
		Camera:       camera,
		ExposureTime: raw["ExposureTime"],
		FNumber:      fNumber,
		ISO:          iso,
		FocalLength:  raw["FocalLength"],
	}, nil
}

// FindUserIDByUsername risolve l'NSID di un utente da username pubblico
// (chiamata anonima, nessun auth richiesto).
func (c *Client) FindUserIDByUsername(username string) (string, error) {
	result, err := c.Call("flickr.people.findByUsername", map[string]string{"username": username})
	if err != nil {
		return "", err
	}
	user, _ := result["user"].(map[string]interface{})
	nsid, _ := user["nsid"].(string)
	return nsid, nil
}

// GetPhotosPage restituisce le foto pubbliche dell'utente. privacy_filter=1
// e' rilevante solo quando autenticati come owner (altrimenti una chiamata
// anonima vede gia' solo le foto pubbliche per definizione).
func (c *Client) GetPhotosPage(userID string, page, perPage int) ([]Photo, error) {
	params := map[string]string{
		"user_id":  userID,
		"extras":   "tags",
		"page":     strconv.Itoa(page),
		"per_page": strconv.Itoa(perPage),
	}
	if c.Authenticated() {
		params["privacy_filter"] = "1"
	}
	result, err := c.Call("flickr.people.getPhotos", params)
	if err != nil {
		return nil, err
	}
	photosObj, _ := result["photos"].(map[string]interface{})
	return parsePhotoList(photosObj), nil
}

// GetPhotosetPhotos restituisce le foto di un album (photoset) specifico.
func (c *Client) GetPhotosetPhotos(photosetID string, page, perPage int) ([]Photo, error) {
	params := map[string]string{
		"photoset_id": photosetID,
		"extras":      "tags",
		"page":        strconv.Itoa(page),
		"per_page":    strconv.Itoa(perPage),
	}
	result, err := c.Call("flickr.photosets.getPhotos", params)
	if err != nil {
		return nil, err
	}
	photosetObj, _ := result["photoset"].(map[string]interface{})
	return parsePhotoList(photosetObj), nil
}

func parsePhotoList(container map[string]interface{}) []Photo {
	rawList, _ := container["photo"].([]interface{})
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
	return photos
}

// PageCount legge photos.pages dall'ultima risposta getPhotos (per Run).
func (c *Client) GetPhotosPageCount(userID string) (int, error) {
	params := map[string]string{"user_id": userID, "page": "1", "per_page": "1"}
	result, err := c.Call("flickr.people.getPhotos", params)
	if err != nil {
		return 0, err
	}
	photosObj, _ := result["photos"].(map[string]interface{})
	pagesF, _ := photosObj["pages"].(float64)
	return int(pagesF), nil
}

// GetPhotoOriginalURL richiede originalsecret/originalformat, presenti solo
// se il client e' autenticato come owner (candownload=1). Senza auth,
// Flickr omette questi campi anche per foto pubbliche.
func (c *Client) GetPhotoOriginalURL(photoID string) (string, error) {
	info, err := c.getPhotoInfo(photoID)
	if err != nil {
		return "", err
	}
	server, _ := info["server"].(string)
	id, _ := info["id"].(string)
	secret, _ := info["originalsecret"].(string)
	format, _ := info["originalformat"].(string)
	if secret == "" || format == "" {
		usage, _ := info["usage"].(map[string]interface{})
		candownload, _ := usage["candownload"].(float64)
		return "", fmt.Errorf("photo %s (candownload=%v): %w", photoID, candownload, ErrOriginalNotAvailable)
	}
	return fmt.Sprintf("https://live.staticflickr.com/%s/%s_%s_o.%s", server, id, secret, format), nil
}

// GetPhotoDisplayURL returns a public "large" (1024px) display-size JPEG
// URL, built from the plain secret/server fields that getInfo always
// returns (no OAuth needed, unlike the original). Used for the viewer
// page's <img>, which doesn't need the full-resolution original.
func (c *Client) GetPhotoDisplayURL(photoID string) (string, error) {
	info, err := c.getPhotoInfo(photoID)
	if err != nil {
		return "", err
	}
	server, _ := info["server"].(string)
	id, _ := info["id"].(string)
	secret, _ := info["secret"].(string)
	return fmt.Sprintf("https://live.staticflickr.com/%s/%s_%s_b.jpg", server, id, secret), nil
}

func (c *Client) GetPhotoOriginalBytes(photoID string) ([]byte, error) {
	url, err := c.GetPhotoOriginalURL(photoID)
	if err != nil {
		return nil, err
	}
	body, status, err := downloadOriginalFunc(url)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("original download failed for %s: HTTP %d from %s", photoID, status, url)
	}
	return body, nil
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

// FindTagID looks up the internal tag ID Flickr assigned to a tag whose
// text matches tagContent on photoID. flickr.photos.removeTag needs this ID
// (not the tag text) — this exists so a tag added via AddTag can be
// reverted.
func (c *Client) FindTagID(photoID, tagContent string) (string, error) {
	info, err := c.getPhotoInfo(photoID)
	if err != nil {
		return "", err
	}
	tagsObj, _ := info["tags"].(map[string]interface{})
	rawList, _ := tagsObj["tag"].([]interface{})
	for _, item := range rawList {
		t, _ := item.(map[string]interface{})
		raw, _ := t["raw"].(string)
		content, _ := t["_content"].(string)
		if raw == tagContent || content == tagContent {
			id, _ := t["id"].(string)
			return id, nil
		}
	}
	return "", fmt.Errorf("tag %q not found on photo %s", tagContent, photoID)
}

// RemoveTag removes a tag by its internal Flickr ID (from FindTagID).
func (c *Client) RemoveTag(photoID, tagID string) error {
	_, err := c.Call("flickr.photos.removeTag", map[string]string{
		"photo_id": photoID,
		"tag_id":   tagID,
	})
	return err
}
