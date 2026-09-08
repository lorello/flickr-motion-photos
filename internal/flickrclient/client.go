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

	"motionphotos/internal/flickroauth"
)

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

func curlDownload(rawURL string) (body []byte, statusCode int, err error) {
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
