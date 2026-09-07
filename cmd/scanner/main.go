// cmd/scanner: CLI that scans a user's public Flickr photos, detects Pixel
// Motion Photos, and simulates (logs) the actions it would publish.
//
// Current state: no OAuth access token configured -> the client only makes
// unsigned read calls (api_key only), which work for public data but cannot
// download the photo original (Flickr always requires owner-authenticated
// OAuth for that, even for public photos). Photos are therefore marked
// "blocked-no-oauth" until the authorization flow runs (Task 8 of the
// plan). Writes to Flickr/R2/git are always simulated at this stage
// (SimulatingWriter/Uploader/Publisher).
package main

import (
	"flag"
	"fmt"
	"os"

	"motionphotos/internal/flickrclient"
	"motionphotos/internal/scanner"
	"motionphotos/internal/statestore"
)

func main() {
	username := flag.String("username", "lorello", "Flickr username to scan")
	limit := flag.Int("limit", 10, "Maximum number of photos to process (-1 = unlimited)")
	viewerBaseURL := flag.String("viewer-base-url", "https://motion.example.com", "Viewer page domain (placeholder until published)")
	cacheDir := flag.String("cache-dir", os.TempDir(), "Local state cache directory")
	flag.Parse()

	apiKey := os.Getenv("FLICKRGO_API_KEY")
	apiSecret := os.Getenv("FLICKRGO_API_SECRET")
	if apiKey == "" || apiSecret == "" {
		fmt.Fprintln(os.Stderr, "FLICKRGO_API_KEY and FLICKRGO_API_SECRET are required in the environment (bw-unlock).")
		os.Exit(1)
	}

	oauthToken := os.Getenv("FLICKR_OAUTH_TOKEN")
	oauthTokenSecret := os.Getenv("FLICKR_OAUTH_TOKEN_SECRET")
	client := flickrclient.NewClient(apiKey, apiSecret, oauthToken, oauthTokenSecret)

	if !client.Authenticated() {
		fmt.Fprintln(os.Stderr, "[INFO] no FLICKR_OAUTH_TOKEN: public read-only mode (api_key). Motion photos will show as 'blocked-no-oauth' until cmd/authorize is run.")
	}

	userID := os.Getenv("FLICKR_USER_ID")
	if userID == "" {
		var err error
		userID, err = client.FindUserIDByUsername(*username)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error resolving user:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "[INFO] FLICKR_USER_ID not set, resolved from username %q: %s\n", *username, userID)
	}

	store, err := statestore.NewFileStore(*cacheDir, userID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error opening state cache:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "[INFO] state cache: %s\n", store.Path())

	writer := scanner.SimulatingWriter{}
	uploader := scanner.SimulatingUploader{PublicBaseURL: "https://videos.example.com"}
	publisher := scanner.SimulatingPublisher{ViewerBaseURL: *viewerBaseURL}

	if err := scanner.Run(client, writer, uploader, publisher, store, userID, *viewerBaseURL, *limit); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
