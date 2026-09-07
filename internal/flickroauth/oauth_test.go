package flickroauth

import "testing"

func TestSignMatchesCanonicalOAuth1Vector(t *testing.T) {
	params := map[string]string{
		"oauth_consumer_key":     "dpf43f3p2l4k3l03",
		"oauth_token":            "nnch734d00sl2jdk",
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_timestamp":        "1191242096",
		"oauth_nonce":            "kllo9940pd9333jh",
		"oauth_version":          "1.0",
		"file":                   "vacation.jpg",
		"size":                   "original",
	}
	sig := Sign("GET", "http://photos.example.net/photos", params, "kd94hf93k423kf44", "pfkkdhi9sl3r4s00")
	want := "tR3+Ty81lMeYAr/Fid0kMTYa/WM="
	if sig != want {
		t.Fatalf("got %s, want %s", sig, want)
	}
}

func TestBuildOAuthParamsWithoutTokenHasNoTokenField(t *testing.T) {
	p := BuildOAuthParams("consumer-key-x", "")
	if p["oauth_consumer_key"] != "consumer-key-x" {
		t.Fatalf("unexpected consumer key: %s", p["oauth_consumer_key"])
	}
	if p["oauth_signature_method"] != "HMAC-SHA1" {
		t.Fatalf("unexpected signature method: %s", p["oauth_signature_method"])
	}
	if p["oauth_version"] != "1.0" {
		t.Fatalf("unexpected version: %s", p["oauth_version"])
	}
	if _, ok := p["oauth_token"]; ok {
		t.Fatal("expected no oauth_token field")
	}
	if len(p["oauth_nonce"]) != 32 {
		t.Fatalf("expected 32-char nonce, got %d", len(p["oauth_nonce"]))
	}
}

func TestBuildOAuthParamsWithTokenIncludesIt(t *testing.T) {
	p := BuildOAuthParams("consumer-key-x", "req-token-abc")
	if p["oauth_token"] != "req-token-abc" {
		t.Fatalf("expected oauth_token req-token-abc, got %s", p["oauth_token"])
	}
}

func TestBuildAuthorizeURLIncludesTokenAndPerms(t *testing.T) {
	url := BuildAuthorizeURL("req-token-123", "write")
	want := "https://www.flickr.com/services/oauth/authorize?oauth_token=req-token-123&perms=write"
	if url != want {
		t.Fatalf("got %s, want %s", url, want)
	}
}
