// cmd/scanner: CLI che scansiona le foto pubbliche Flickr di un utente,
// rileva le Motion Photo Pixel e simula (logga) le azioni che pubblicherebbe.
//
// Stato attuale: nessun access token OAuth configurato -> il client fa solo
// chiamate di lettura non firmate (api_key only), che funzionano per dati
// pubblici ma non possono scaricare l'originale della foto (Flickr richiede
// sempre OAuth owner-autenticato per quello, anche su foto pubbliche).
// Le foto vengono quindi marcate "blocked-no-oauth" finché non si esegue il
// flusso di autorizzazione (Task 8 del piano). Scritture verso Flickr/R2/git
// sono sempre simulate in questa fase (SimulatingWriter/Uploader/Publisher).
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
	username := flag.String("username", "lorello", "Username Flickr da scansionare")
	limit := flag.Int("limit", 10, "Numero massimo di foto da processare (-1 = illimitato)")
	viewerBaseURL := flag.String("viewer-base-url", "https://motion.example.com", "Dominio della pagina viewer (placeholder finché non pubblicata)")
	cacheDir := flag.String("cache-dir", os.TempDir(), "Directory della cache di stato locale")
	flag.Parse()

	apiKey := os.Getenv("FLICKRGO_API_KEY")
	apiSecret := os.Getenv("FLICKRGO_API_SECRET")
	if apiKey == "" || apiSecret == "" {
		fmt.Fprintln(os.Stderr, "Servono FLICKRGO_API_KEY e FLICKRGO_API_SECRET nell'ambiente (bw-unlock).")
		os.Exit(1)
	}

	oauthToken := os.Getenv("FLICKR_OAUTH_TOKEN")
	oauthTokenSecret := os.Getenv("FLICKR_OAUTH_TOKEN_SECRET")
	client := flickrclient.NewClient(apiKey, apiSecret, oauthToken, oauthTokenSecret)

	if !client.Authenticated() {
		fmt.Fprintln(os.Stderr, "[INFO] nessun FLICKR_OAUTH_TOKEN: modalità sola lettura pubblica (api_key). Le foto motion risulteranno 'blocked-no-oauth' finché non si esegue cmd/authorize.")
	}

	userID := os.Getenv("FLICKR_USER_ID")
	if userID == "" {
		var err error
		userID, err = client.FindUserIDByUsername(*username)
		if err != nil {
			fmt.Fprintln(os.Stderr, "errore risolvendo l'utente:", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "[INFO] FLICKR_USER_ID non impostato, risolto da username %q: %s\n", *username, userID)
	}

	store, err := statestore.NewFileStore(*cacheDir, userID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "errore aprendo la cache di stato:", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "[INFO] cache di stato: %s\n", store.Path())

	writer := scanner.SimulatingWriter{}
	uploader := scanner.SimulatingUploader{PublicBaseURL: "https://videos.example.com"}
	publisher := scanner.SimulatingPublisher{ViewerBaseURL: *viewerBaseURL}

	if err := scanner.Run(client, writer, uploader, publisher, store, userID, *viewerBaseURL, *limit); err != nil {
		fmt.Fprintln(os.Stderr, "errore:", err)
		os.Exit(1)
	}
}
