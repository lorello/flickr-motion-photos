package plausible

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func withDoRequest(t *testing.T, fn func(*http.Request) (*http.Response, error)) {
	t.Helper()
	orig := doRequestFunc
	doRequestFunc = fn
	t.Cleanup(func() { doRequestFunc = orig })
}

func jsonResponse(body map[string]interface{}) *http.Response {
	b, _ := json.Marshal(body)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(b)))}
}

func TestGetPageViewsParsesVisitorsAndPageviews(t *testing.T) {
	var capturedAuth string
	var capturedBody map[string]interface{}
	withDoRequest(t, func(req *http.Request) (*http.Response, error) {
		capturedAuth = req.Header.Get("Authorization")
		json.NewDecoder(req.Body).Decode(&capturedBody)
		return jsonResponse(map[string]interface{}{
			"results": []map[string]interface{}{
				{"metrics": []float64{2, 38}},
			},
		}), nil
	})

	visitors, pageviews, err := GetPageViews("secret-key", "flickr-utils.lorello.it", "/photos/lorello/123.html")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if visitors != 2 {
		t.Fatalf("unexpected visitors: %d", visitors)
	}
	if pageviews != 38 {
		t.Fatalf("unexpected pageviews: %d", pageviews)
	}
	if capturedAuth != "Bearer secret-key" {
		t.Fatalf("unexpected auth header: %s", capturedAuth)
	}
	if capturedBody["site_id"] != "flickr-utils.lorello.it" {
		t.Fatalf("unexpected site_id: %v", capturedBody["site_id"])
	}
}

func TestGetPageViewsReturnsZeroWhenNoResults(t *testing.T) {
	withDoRequest(t, func(req *http.Request) (*http.Response, error) {
		return jsonResponse(map[string]interface{}{"results": []map[string]interface{}{}}), nil
	})

	visitors, pageviews, err := GetPageViews("k", "site", "/p/999.html")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if visitors != 0 || pageviews != 0 {
		t.Fatalf("expected zero values, got %d/%d", visitors, pageviews)
	}
}

func TestGetPageViewsFailsOnNonOKStatus(t *testing.T) {
	withDoRequest(t, func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(strings.NewReader(""))}, nil
	})

	_, _, err := GetPageViews("bad-key", "site", "/p/999.html")
	if err == nil {
		t.Fatal("expected error on non-200 status")
	}
}
