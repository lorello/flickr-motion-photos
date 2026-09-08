// cmd/republish: re-renders and re-uploads the viewer page for every
// already-published photo (from the local state cache), using the current
// site/template.html. Use this after a template change — it does not touch
// Flickr (description/tags are already correct and untouched) or re-upload
// videos (unchanged), only the HTML page.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"motionphotos/internal/flickrclient"
	"motionphotos/internal/r2upload"
	"motionphotos/internal/sitegen"
	"motionphotos/internal/statestore"
)

func main() {
	username := flag.String("username", "lorello", "Flickr username (used to resolve FLICKR_USER_ID and build photo page links)")
	cacheDir := flag.String("cache-dir", os.TempDir(), "Local state cache directory")
	repoRoot := flag.String("repo-root", ".", "Repository root containing site/template.html")
	flag.Parse()

	apiKey := os.Getenv("FLICKRGO_API_KEY")
	apiSecret := os.Getenv("FLICKRGO_API_SECRET")
	oauthToken := os.Getenv("FLICKR_OAUTH_TOKEN")
	oauthTokenSecret := os.Getenv("FLICKR_OAUTH_TOKEN_SECRET")
	if apiKey == "" || apiSecret == "" {
		fmt.Fprintln(os.Stderr, "FLICKRGO_API_KEY and FLICKRGO_API_SECRET are required in the environment.")
		os.Exit(1)
	}
	client := flickrclient.NewClient(apiKey, apiSecret, oauthToken, oauthTokenSecret)

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
			fmt.Fprintf(os.Stderr, "%s is required in the environment.\n", name)
			os.Exit(1)
		}
	}

	r2Client, err := r2upload.NewClient(context.Background(), r2AccountID, r2AccessKeyID, r2SecretAccessKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error creating R2 client:", err)
		os.Exit(1)
	}
	publisher := r2upload.PageUploader{Client: r2Client, Bucket: r2Bucket, PublicBaseURL: r2PublicBaseURL}

	templateBytes, err := os.ReadFile(filepath.Join(*repoRoot, "site", "template.html"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error reading site/template.html:", err)
		os.Exit(1)
	}

	userID := os.Getenv("FLICKR_USER_ID")
	if userID == "" {
		userID, err = client.FindUserIDByUsername(*username)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error resolving user:", err)
			os.Exit(1)
		}
	}

	store, err := statestore.NewFileStore(*cacheDir, userID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error opening state cache:", err)
		os.Exit(1)
	}

	photoIDs := store.PhotoIDsByStatus("published")
	fmt.Fprintf(os.Stderr, "[INFO] republishing %d photo(s)\n", len(photoIDs))

	for _, photoID := range photoIDs {
		result, err := client.Call("flickr.photos.getInfo", map[string]string{"photo_id": photoID})
		if err != nil {
			fmt.Fprintln(os.Stderr, "FAIL getInfo", photoID, err)
			continue
		}
		photo, _ := result["photo"].(map[string]interface{})
		titleObj, _ := photo["title"].(map[string]interface{})
		title, _ := titleObj["_content"].(string)

		imageURL, err := client.GetPhotoDisplayURL(photoID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "FAIL display url", photoID, err)
			continue
		}
		details, err := client.GetPhotoDetails(photoID)
		if err != nil {
			fmt.Fprintln(os.Stderr, "FAIL details", photoID, err)
			continue
		}
		exif, _ := client.GetPhotoExif(photoID) // supplementary, degrade gracefully
		videoURL := r2PublicBaseURL + "/" + photoID + ".mp4"

		html, err := sitegen.RenderPhotoPage(string(templateBytes), sitegen.PageData{
			PhotoID:        photoID,
			Title:          title,
			ImageURL:       imageURL,
			VideoURL:       videoURL,
			PhotoPageURL:   fmt.Sprintf("https://www.flickr.com/photos/%s/%s/", *username, photoID),
			OwnerName:      details.OwnerName,
			OwnerAvatarURL: details.OwnerAvatarURL,
			DateTaken:      details.DateTaken,
			Tags:           details.Tags,
			Views:          details.Views,
			Camera:         exif.Camera,
			ExposureTime:   exif.ExposureTime,
			FNumber:        exif.FNumber,
			ISO:            exif.ISO,
			FocalLength:    exif.FocalLength,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "FAIL render", photoID, err)
			continue
		}
		url, err := publisher.PublishPage(photoID, html)
		if err != nil {
			fmt.Fprintln(os.Stderr, "FAIL publish", photoID, err)
			continue
		}
		fmt.Println(url)
	}
}
