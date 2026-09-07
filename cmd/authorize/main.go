// cmd/authorize: script one-off per ottenere un access token OAuth 1.0a
// dall'account Flickr proprietario. Flusso interattivo: apre un URL nel
// browser, l'utente autorizza l'app, incolla il verifier mostrato da Flickr.
// I due valori stampati alla fine vanno salvati in Bitwarden "Personal
// Secrets" come FLICKR_OAUTH_TOKEN / FLICKR_OAUTH_TOKEN_SECRET.
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
		fmt.Fprintln(os.Stderr, "Servono FLICKRGO_API_KEY e FLICKRGO_API_SECRET nell'ambiente (bw-unlock).")
		os.Exit(1)
	}

	request, err := flickroauth.GetRequestToken(consumerKey, consumerSecret)
	if err != nil {
		fmt.Fprintln(os.Stderr, "errore ottenendo il request token:", err)
		os.Exit(1)
	}
	requestToken := request["oauth_token"]
	requestTokenSecret := request["oauth_token_secret"]
	if requestToken == "" || requestTokenSecret == "" {
		fmt.Fprintf(os.Stderr, "risposta inattesa da Flickr: %v\n", request)
		os.Exit(1)
	}

	fmt.Println("Apri questo URL, autorizza l'app, e incolla qui il verifier mostrato da Flickr:")
	fmt.Println(flickroauth.BuildAuthorizeURL(requestToken, "write"))
	fmt.Print("Verifier: ")

	reader := bufio.NewReader(os.Stdin)
	verifierLine, _ := reader.ReadString('\n')
	verifier := strings.TrimSpace(verifierLine)
	if verifier == "" {
		fmt.Fprintln(os.Stderr, "verifier vuoto, interrotto.")
		os.Exit(1)
	}

	access, err := flickroauth.GetAccessToken(consumerKey, consumerSecret, requestToken, requestTokenSecret, verifier)
	if err != nil {
		fmt.Fprintln(os.Stderr, "errore ottenendo l'access token:", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println("Autenticato come:", access["username"], "(NSID:", access["user_nsid"]+")")
	fmt.Println()
	fmt.Println("Salva questi due valori in Bitwarden 'Personal Secrets' come campi custom:")
	fmt.Printf("  FLICKR_OAUTH_TOKEN = %s\n", access["oauth_token"])
	fmt.Printf("  FLICKR_OAUTH_TOKEN_SECRET = %s\n", access["oauth_token_secret"])
	fmt.Println()
	fmt.Println("Poi esegui bw-unlock in una nuova shell per caricarli.")
}
