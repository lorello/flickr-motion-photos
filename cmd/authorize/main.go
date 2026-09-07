// cmd/authorize: one-off script to obtain an OAuth 1.0a access token from
// the owner's Flickr account. Interactive flow: opens a URL in the
// browser, the user authorizes the app, pastes the verifier Flickr shows.
// The two values printed at the end should be saved in Bitwarden "Personal
// Secrets" as FLICKR_OAUTH_TOKEN / FLICKR_OAUTH_TOKEN_SECRET.
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
		fmt.Fprintln(os.Stderr, "FLICKRGO_API_KEY and FLICKRGO_API_SECRET are required in the environment (bw-unlock).")
		os.Exit(1)
	}

	request, err := flickroauth.GetRequestToken(consumerKey, consumerSecret)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error getting request token:", err)
		os.Exit(1)
	}
	requestToken := request["oauth_token"]
	requestTokenSecret := request["oauth_token_secret"]
	if requestToken == "" || requestTokenSecret == "" {
		fmt.Fprintf(os.Stderr, "unexpected response from Flickr: %v\n", request)
		os.Exit(1)
	}

	fmt.Println("Open this URL, authorize the app, then paste the verifier Flickr shows you:")
	fmt.Println(flickroauth.BuildAuthorizeURL(requestToken, "write"))
	fmt.Print("Verifier: ")

	reader := bufio.NewReader(os.Stdin)
	verifierLine, _ := reader.ReadString('\n')
	verifier := strings.TrimSpace(verifierLine)
	if verifier == "" {
		fmt.Fprintln(os.Stderr, "empty verifier, aborting.")
		os.Exit(1)
	}

	access, err := flickroauth.GetAccessToken(consumerKey, consumerSecret, requestToken, requestTokenSecret, verifier)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error getting access token:", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("Authenticated as:", access["username"], "(NSID:", access["user_nsid"]+")")
	fmt.Println()
	fmt.Println("Save these two values in Bitwarden 'Personal Secrets' as custom fields:")
	fmt.Printf("  FLICKR_OAUTH_TOKEN = %s\n", access["oauth_token"])
	fmt.Printf("  FLICKR_OAUTH_TOKEN_SECRET = %s\n", access["oauth_token_secret"])
	fmt.Println()
	fmt.Println("Then run bw-unlock in a new shell to load them.")
}
