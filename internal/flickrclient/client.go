package flickrclient

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"motionphotos/internal/flickroauth"
)

const apiURL = "https://api.flickr.com/services/rest"

// signedGetFunc, unsignedGetFunc e httpGetFunc permettono ai test di
// sostituire il livello HTTP.
var signedGetFunc = flickroauth.SignedGet
var httpGetFunc = http.Get

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
		return "", fmt.Errorf("originale non disponibile per la foto %s (candownload=%v): serve OAuth owner-autenticato", photoID, candownload)
	}
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
