// cmd/scanner: CLI that scans a user's public Flickr photos, detects Pixel
// Motion Photos, uploads extracted videos and generated viewer pages to a
// Cloudflare R2 bucket, and writes the viewer link into the photo's Flickr
// description + a status tag.
//
// Real writes by default — pass --dry-run to simulate (SimulatingWriter)
// instead of writing to Flickr for real.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"motionphotos/internal/flickrclient"
	"motionphotos/internal/r2upload"
	"motionphotos/internal/scanner"
	"motionphotos/internal/statestore"
)

func main() {
	username := flag.String("username", "lorello", "Flickr username to scan")
	limit := flag.Int("limit", 10, "Maximum number of photos to process (-1 = unlimited)")
	cacheDir := flag.String("cache-dir", os.TempDir(), "Local state cache directory")
	repoRoot := flag.String("repo-root", ".", "Repository root containing site/template.html")
	photosetID := flag.String("photoset-id", "", "If set, scan only this album instead of the whole photostream")
	dryRun := flag.Bool("dry-run", false, "Simulate Flickr description/tag writes instead of writing for real")
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

	r2AccountID := os.Getenv("R2_ACCOUNT_ID")
	r2AccessKeyID := os.Getenv("R2_ACCESS_KEY_ID")
	r2SecretAccessKey := os.Getenv("R2_SECRET_ACCESS_KEY")
	r2Bucket := os.Getenv("R2_BUCKET")
	r2PublicBaseURL := os.Getenv("R2_PUBLIC_BASE_URL")
	for name, value := range map[string]string{
		"R2_ACCOUNT_ID": r2AccountID, "R2_ACCESS_KEY_ID": r2AccessKeyID,
		"R2_SECRET_ACCESS_KEY": r2SecretAccessKey, "R2_BUCKET": r2Bucket,
		"R2_PUBLIC_BASE_URL": r2PublicBaseURL,
	} {
		if value == "" {
			fmt.Fprintf(os.Stderr, "%s is required in the environment (bw-unlock, or .env).\n", name)
			os.Exit(1)
		}
	}

	r2Client, err := r2upload.NewClient(context.Background(), r2AccountID, r2AccessKeyID, r2SecretAccessKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error creating R2 client:", err)
		os.Exit(1)
	}

	templateBytes, err := os.ReadFile(filepath.Join(*repoRoot, "site", "template.html"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error reading site/template.html:", err)
		os.Exit(1)
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

	var writer scanner.Writer = client
	if *dryRun {
		fmt.Fprintln(os.Stderr, "[INFO] --dry-run: Flickr description/tag writes will be simulated, not real.")
		writer = scanner.SimulatingWriter{}
	} else {
		fmt.Fprintln(os.Stderr, "[INFO] Flickr description/tags will be written for real (pass --dry-run to simulate).")
	}
	uploader := r2upload.VideoUploader{Client: r2Client, Bucket: r2Bucket, PublicBaseURL: r2PublicBaseURL}
	publisher := r2upload.PageUploader{Client: r2Client, Bucket: r2Bucket, PublicBaseURL: r2PublicBaseURL}

	if *photosetID != "" {
		photos, err := client.GetPhotosetPhotos(*photosetID, 1, 500)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error listing photoset:", err)
			os.Exit(1)
		}
		processed := 0
		for _, photo := range photos {
			if *limit >= 0 && processed >= *limit {
				break
			}
			status, err := scanner.ProcessPhoto(client, writer, uploader, publisher, store, string(templateBytes), *username, photo)
			if err != nil {
				fmt.Fprintln(os.Stderr, "error:", err)
				os.Exit(1)
			}
			fmt.Printf("%s\t%s\t%s\n", photo.ID, status, photo.Title)
			if status != "skipped(flickr-tag)" && status != "skipped(cache)" {
				processed++
			}
		}
		return
	}

	if err := scanner.Run(client, writer, uploader, publisher, store, userID, *username, string(templateBytes), *limit); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
