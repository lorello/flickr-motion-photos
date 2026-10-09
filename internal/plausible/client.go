// Package plausible reads page-level pageview counts from Plausible's
// Stats API (v2), for showing "video page views" on the viewer page itself
// — distinct from Flickr's own view counter, which counts views of the
// Flickr photo, not of this page.
package plausible

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
)

const queryURL = "https://plausible.io/api/v2/query"

// doRequestFunc is injectable in tests.
var doRequestFunc = http.DefaultClient.Do

type queryRequest struct {
	SiteID    string          `json:"site_id"`
	Metrics   []string        `json:"metrics"`
	DateRange string          `json:"date_range"`
	Filters   [][]interface{} `json:"filters"`
}

type queryResponse struct {
	Results []struct {
		Metrics []float64 `json:"metrics"`
	} `json:"results"`
}

// GetPageViews returns (visitors, pageviews) for path on siteID, all-time.
// path must be the page's path only (e.g. "/photos/lorello/123.html"), not
// the full URL.
func GetPageViews(apiKey, siteID, path string) (visitors, pageviews int, err error) {
	reqBody := queryRequest{
		SiteID:    siteID,
		Metrics:   []string{"visitors", "pageviews"},
		DateRange: "all",
		Filters:   [][]interface{}{{"is", "event:page", []string{path}}},
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return 0, 0, err
	}

	req, err := http.NewRequest(http.MethodPost, queryURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return 0, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := doRequestFunc(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, 0, fmt.Errorf("plausible query failed: HTTP %d", resp.StatusCode)
	}

	var parsed queryResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return 0, 0, err
	}
	if len(parsed.Results) == 0 || len(parsed.Results[0].Metrics) < 2 {
		return 0, 0, nil
	}
	return int(parsed.Results[0].Metrics[0]), int(parsed.Results[0].Metrics[1]), nil
}
