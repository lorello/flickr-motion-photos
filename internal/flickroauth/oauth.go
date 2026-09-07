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
// oauth_token dal risultato.
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
