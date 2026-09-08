package flickrclient

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func makeClient() *Client {
	return NewClient("ck", "cs", "", "")
}

func withUnsignedGet(t *testing.T, fn func(rawURL string, params map[string]string) (string, error)) {
	t.Helper()
	orig := unsignedGetFunc
	unsignedGetFunc = fn
	t.Cleanup(func() { unsignedGetFunc = orig })
}

func withDownloadOriginal(t *testing.T, fn func(rawURL string) ([]byte, int, error)) {
	t.Helper()
	orig := downloadOriginalFunc
	downloadOriginalFunc = fn
	t.Cleanup(func() { downloadOriginalFunc = orig })
}

func jsonBody(v map[string]interface{}) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestCallRaisesOnFailStatus(t *testing.T) {
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		return jsonBody(map[string]interface{}{"stat": "fail", "code": 1, "message": "boom"}), nil
	})
	client := makeClient()
	_, err := client.Call("flickr.test.echo", nil)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("expected error 'boom', got %v", err)
	}
}

func TestCallReturnsParsedJSONOnSuccessUnauthenticated(t *testing.T) {
	var capturedParams map[string]string
	withUnsignedGet(t, func(_ string, params map[string]string) (string, error) {
		capturedParams = params
		return jsonBody(map[string]interface{}{"stat": "ok", "value": 42}), nil
	})
	client := makeClient()
	result, err := client.Call("flickr.test.echo", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["value"].(float64) != 42 {
		t.Fatalf("unexpected value: %v", result["value"])
	}
	if capturedParams["api_key"] != "ck" {
		t.Fatalf("expected api_key set, got %q", capturedParams["api_key"])
	}
}

func TestGetPhotosPageExtractsIDTitleTagsWithoutPrivacyFilterWhenAnonymous(t *testing.T) {
	var capturedParams map[string]string
	withUnsignedGet(t, func(_ string, params map[string]string) (string, error) {
		capturedParams = params
		return jsonBody(map[string]interface{}{
			"stat": "ok",
			"photos": map[string]interface{}{
				"photo": []interface{}{
					map[string]interface{}{"id": "111", "title": "PXL_1.MP", "tags": "flickrmp:status=checked vacation"},
					map[string]interface{}{"id": "222", "title": "PXL_2.MP", "tags": ""},
				},
			},
		}), nil
	})
	client := makeClient()
	photos, err := client.GetPhotosPage("65791659@N00", 1, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []Photo{
		{ID: "111", Title: "PXL_1.MP", Tags: []string{"flickrmp:status=checked", "vacation"}},
		{ID: "222", Title: "PXL_2.MP", Tags: []string{}},
	}
	if !reflect.DeepEqual(photos, want) {
		t.Fatalf("got %+v, want %+v", photos, want)
	}
	if _, ok := capturedParams["privacy_filter"]; ok {
		t.Fatalf("expected no privacy_filter param when unauthenticated, got %q", capturedParams["privacy_filter"])
	}
}

func TestGetPhotosetPhotosExtractsIDTitleTags(t *testing.T) {
	var capturedParams map[string]string
	withUnsignedGet(t, func(_ string, params map[string]string) (string, error) {
		capturedParams = params
		return jsonBody(map[string]interface{}{
			"stat": "ok",
			"photoset": map[string]interface{}{
				"id": "72177720334659015",
				"photo": []interface{}{
					map[string]interface{}{"id": "333", "title": "PXL_3.MP", "tags": "flickrmp:status=checked"},
				},
			},
		}), nil
	})
	client := makeClient()
	photos, err := client.GetPhotosetPhotos("72177720334659015", 1, 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []Photo{{ID: "333", Title: "PXL_3.MP", Tags: []string{"flickrmp:status=checked"}}}
	if !reflect.DeepEqual(photos, want) {
		t.Fatalf("got %+v, want %+v", photos, want)
	}
	if capturedParams["method"] != "flickr.photosets.getPhotos" {
		t.Fatalf("unexpected method: %s", capturedParams["method"])
	}
	if capturedParams["photoset_id"] != "72177720334659015" {
		t.Fatalf("unexpected photoset_id: %s", capturedParams["photoset_id"])
	}
}

func TestGetPhotoOriginalURLFailsWithoutOAuthWhenFieldsMissing(t *testing.T) {
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		return jsonBody(map[string]interface{}{
			"stat":  "ok",
			"photo": map[string]interface{}{"id": "999", "server": "65535", "usage": map[string]interface{}{"candownload": float64(0)}},
		}), nil
	})
	client := makeClient()
	_, err := client.GetPhotoOriginalURL("999")
	if err == nil {
		t.Fatal("expected error when originalsecret/originalformat are missing")
	}
	if !errors.Is(err, ErrOriginalNotAvailable) {
		t.Fatalf("expected ErrOriginalNotAvailable, got %v", err)
	}
}

func TestGetPhotoOriginalBytesDownloadsFromResolvedURL(t *testing.T) {
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		return jsonBody(map[string]interface{}{
			"stat":  "ok",
			"photo": map[string]interface{}{"id": "999", "server": "65535", "originalsecret": "abc123", "originalformat": "jpg"},
		}), nil
	})
	var capturedURL string
	withDownloadOriginal(t, func(rawURL string) ([]byte, int, error) {
		capturedURL = rawURL
		return []byte("JPEGBYTES"), 200, nil
	})

	client := makeClient()
	data, err := client.GetPhotoOriginalBytes("999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "JPEGBYTES" {
		t.Fatalf("got %q", data)
	}
	want := "https://live.staticflickr.com/65535/999_abc123_o.jpg"
	if capturedURL != want {
		t.Fatalf("got %s, want %s", capturedURL, want)
	}
}

func TestGetPhotoOriginalBytesFailsOnNonOKStatus(t *testing.T) {
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		return jsonBody(map[string]interface{}{
			"stat":  "ok",
			"photo": map[string]interface{}{"id": "999", "server": "65535", "originalsecret": "abc123", "originalformat": "jpg"},
		}), nil
	})
	withDownloadOriginal(t, func(rawURL string) ([]byte, int, error) {
		return []byte("<html>502 Bad Gateway</html>"), 502, nil
	})

	client := makeClient()
	_, err := client.GetPhotoOriginalBytes("999")
	if err == nil {
		t.Fatal("expected error on non-200 download status")
	}
}

func TestFindUserIDByUsername(t *testing.T) {
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		return jsonBody(map[string]interface{}{
			"stat": "ok",
			"user": map[string]interface{}{"nsid": "65791659@N00", "id": "65791659@N00"},
		}), nil
	})
	client := makeClient()
	nsid, err := client.FindUserIDByUsername("lorello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nsid != "65791659@N00" {
		t.Fatalf("got %s", nsid)
	}
}

func TestGetPhotoDisplayURLBuildsLargeSizeURLWithoutOAuth(t *testing.T) {
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		return jsonBody(map[string]interface{}{
			"stat":  "ok",
			"photo": map[string]interface{}{"id": "999", "server": "65535", "secret": "abc123"},
		}), nil
	})
	client := makeClient()
	url, err := client.GetPhotoDisplayURL("999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "https://live.staticflickr.com/65535/999_abc123_b.jpg"
	if url != want {
		t.Fatalf("got %s, want %s", url, want)
	}
}

func TestGetPhotoDetailsExtractsOwnerAvatarDateAndFiltersInternalTags(t *testing.T) {
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		return jsonBody(map[string]interface{}{
			"stat": "ok",
			"photo": map[string]interface{}{
				"owner": map[string]interface{}{
					"nsid": "65791659@N00", "username": "lorello",
					"iconserver": "5348", "iconfarm": float64(6),
				},
				"dates":    map[string]interface{}{"taken": "2026-07-11 11:15:21"},
				"views":    "1033",
				"comments": map[string]interface{}{"_content": "3"},
				"tags": map[string]interface{}{
					"tag": []interface{}{
						map[string]interface{}{"_content": "vacation"},
						map[string]interface{}{"_content": "flickrmp:status=published"},
					},
				},
			},
		}), nil
	})
	client := makeClient()
	details, err := client.GetPhotoDetails("999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if details.OwnerName != "lorello" {
		t.Fatalf("unexpected owner name: %s", details.OwnerName)
	}
	wantAvatar := "https://farm6.staticflickr.com/5348/buddyicons/65791659@N00.jpg"
	if details.OwnerAvatarURL != wantAvatar {
		t.Fatalf("got %s, want %s", details.OwnerAvatarURL, wantAvatar)
	}
	if details.DateTaken != "2026-07-11 11:15:21" {
		t.Fatalf("unexpected date taken: %s", details.DateTaken)
	}
	if details.Views != "1033" {
		t.Fatalf("unexpected views: %s", details.Views)
	}
	if details.Comments != "3" {
		t.Fatalf("unexpected comments: %s", details.Comments)
	}
	if len(details.Tags) != 1 || details.Tags[0] != "vacation" {
		t.Fatalf("expected only 'vacation' tag (internal flickrmp: tag filtered out), got %v", details.Tags)
	}
}

func TestGetPhotoDetailsUsesDefaultAvatarWhenNoIconServer(t *testing.T) {
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		return jsonBody(map[string]interface{}{
			"stat": "ok",
			"photo": map[string]interface{}{
				"owner": map[string]interface{}{"nsid": "999@N00", "username": "someone", "iconserver": "0"},
				"dates": map[string]interface{}{"taken": ""},
				"tags":  map[string]interface{}{"tag": []interface{}{}},
			},
		}), nil
	})
	client := makeClient()
	details, err := client.GetPhotoDetails("999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if details.OwnerAvatarURL != "https://www.flickr.com/images/buddyicon.gif" {
		t.Fatalf("unexpected default avatar: %s", details.OwnerAvatarURL)
	}
}

func TestGetPhotoExifExtractsCameraAndFormattedExposure(t *testing.T) {
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		return jsonBody(map[string]interface{}{
			"stat": "ok",
			"photo": map[string]interface{}{
				"camera": "Google Pixel Fold",
				"exif": []interface{}{
					map[string]interface{}{"tag": "ExposureTime", "raw": map[string]interface{}{"_content": "1/1248"}},
					map[string]interface{}{"tag": "FNumber", "raw": map[string]interface{}{"_content": "1.7"}},
					map[string]interface{}{"tag": "ISO", "raw": map[string]interface{}{"_content": "44"}},
					map[string]interface{}{"tag": "FocalLength", "raw": map[string]interface{}{"_content": "4.5 mm"}},
				},
			},
		}), nil
	})
	client := makeClient()
	exif, err := client.GetPhotoExif("999")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exif.Camera != "Google Pixel Fold" {
		t.Fatalf("unexpected camera: %s", exif.Camera)
	}
	if exif.ExposureTime != "1/1248" {
		t.Fatalf("unexpected exposure time: %s", exif.ExposureTime)
	}
	if exif.FNumber != "f/1.7" {
		t.Fatalf("unexpected f-number: %s", exif.FNumber)
	}
	if exif.ISO != "ISO 44" {
		t.Fatalf("unexpected iso: %s", exif.ISO)
	}
	if exif.FocalLength != "4.5 mm" {
		t.Fatalf("unexpected focal length: %s", exif.FocalLength)
	}
}

func TestAddTagCallsAddTags(t *testing.T) {
	var capturedParams map[string]string
	withUnsignedGet(t, func(_ string, params map[string]string) (string, error) {
		capturedParams = params
		return jsonBody(map[string]interface{}{"stat": "ok"}), nil
	})
	client := makeClient()
	if err := client.AddTag("999", "flickrmp:status=published"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedParams["method"] != "flickr.photos.addTags" {
		t.Fatalf("unexpected method: %s", capturedParams["method"])
	}
	if capturedParams["tags"] != `"flickrmp:status=published"` {
		t.Fatalf("unexpected tags: %s", capturedParams["tags"])
	}
}

func TestFindTagIDMatchesByRawOrContent(t *testing.T) {
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		return jsonBody(map[string]interface{}{
			"stat": "ok",
			"photo": map[string]interface{}{
				"tags": map[string]interface{}{
					"tag": []interface{}{
						map[string]interface{}{"id": "111-222", "raw": "flickrmp:status=published", "_content": "flickrmp:status=published"},
						map[string]interface{}{"id": "333-444", "raw": "vacation", "_content": "vacation"},
					},
				},
			},
		}), nil
	})
	client := makeClient()
	id, err := client.FindTagID("999", "flickrmp:status=published")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "111-222" {
		t.Fatalf("got %s", id)
	}
}

func TestFindTagIDErrorsWhenNotFound(t *testing.T) {
	withUnsignedGet(t, func(string, map[string]string) (string, error) {
		return jsonBody(map[string]interface{}{
			"stat":  "ok",
			"photo": map[string]interface{}{"tags": map[string]interface{}{"tag": []interface{}{}}},
		}), nil
	})
	client := makeClient()
	_, err := client.FindTagID("999", "flickrmp:status=published")
	if err == nil {
		t.Fatal("expected error when tag not found")
	}
}

func TestRemoveTagCallsRemoveTagWithTagID(t *testing.T) {
	var capturedParams map[string]string
	withUnsignedGet(t, func(_ string, params map[string]string) (string, error) {
		capturedParams = params
		return jsonBody(map[string]interface{}{"stat": "ok"}), nil
	})
	client := makeClient()
	if err := client.RemoveTag("999", "111-222"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedParams["method"] != "flickr.photos.removeTag" {
		t.Fatalf("unexpected method: %s", capturedParams["method"])
	}
	if capturedParams["tag_id"] != "111-222" {
		t.Fatalf("unexpected tag_id: %s", capturedParams["tag_id"])
	}
}
