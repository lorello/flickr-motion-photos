package r2upload

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type fakePutObjectAPI struct {
	gotInput *s3.PutObjectInput
}

func (f *fakePutObjectAPI) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.gotInput = params
	return &s3.PutObjectOutput{}, nil
}

func TestPutObjectSetsKeyBodyAndContentType(t *testing.T) {
	fake := &fakePutObjectAPI{}
	url, err := PutObject(context.Background(), fake, "bucket", "https://videos.example.com", "12345.mp4", "video/mp4", []byte("FAKEVIDEO"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *fake.gotInput.Bucket != "bucket" {
		t.Fatalf("unexpected bucket: %s", *fake.gotInput.Bucket)
	}
	if *fake.gotInput.Key != "12345.mp4" {
		t.Fatalf("unexpected key: %s", *fake.gotInput.Key)
	}
	if *fake.gotInput.ContentType != "video/mp4" {
		t.Fatalf("unexpected content type: %s", *fake.gotInput.ContentType)
	}
	body, _ := io.ReadAll(fake.gotInput.Body)
	if !bytes.Equal(body, []byte("FAKEVIDEO")) {
		t.Fatalf("unexpected body: %s", body)
	}
	if url != "https://videos.example.com/12345.mp4" {
		t.Fatalf("unexpected url: %s", url)
	}
}

func TestVideoUploaderUsesFlatKeyAndContentType(t *testing.T) {
	fake := &fakePutObjectAPI{}
	u := VideoUploader{Client: fake, Bucket: "bucket", PublicBaseURL: "https://videos.example.com"}
	url, err := u.UploadVideo("999", []byte("V"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *fake.gotInput.Key != "999.mp4" {
		t.Fatalf("unexpected key: %s", *fake.gotInput.Key)
	}
	if url != "https://videos.example.com/999.mp4" {
		t.Fatalf("unexpected url: %s", url)
	}
}

func TestPageUploaderUsesUsernamePrefixedKeyAndHTMLContentType(t *testing.T) {
	fake := &fakePutObjectAPI{}
	p := PageUploader{Client: fake, Bucket: "bucket", PublicBaseURL: "https://videos.example.com", Username: "lorello"}
	url, err := p.PublishPage("999", "<html></html>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *fake.gotInput.Key != "photos/lorello/999.html" {
		t.Fatalf("unexpected key: %s", *fake.gotInput.Key)
	}
	if *fake.gotInput.ContentType != "text/html; charset=utf-8" {
		t.Fatalf("unexpected content type: %s", *fake.gotInput.ContentType)
	}
	if url != "https://videos.example.com/photos/lorello/999.html" {
		t.Fatalf("unexpected url: %s", url)
	}
}

type recordingPutObjectAPI struct {
	calls []*s3.PutObjectInput
}

func (f *recordingPutObjectAPI) PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.calls = append(f.calls, params)
	return &s3.PutObjectOutput{}, nil
}

func TestUploadStaticAssetsUploadsCSSAndJSWithCorrectKeysAndTypes(t *testing.T) {
	dir := t.TempDir()
	siteDir := dir + "/site"
	if err := os.MkdirAll(siteDir, 0o755); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(siteDir+"/style.css", []byte("body{color:red}"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := os.WriteFile(siteDir+"/script.js", []byte("console.log(1)"), 0o644); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fake := &recordingPutObjectAPI{}
	if err := UploadStaticAssets(context.Background(), fake, "bucket", "https://videos.example.com", dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.calls) != 2 {
		t.Fatalf("expected 2 PutObject calls, got %d", len(fake.calls))
	}

	byKey := map[string]*s3.PutObjectInput{}
	for _, call := range fake.calls {
		byKey[*call.Key] = call
	}

	css, ok := byKey["style.css"]
	if !ok {
		t.Fatal("expected an upload with key style.css")
	}
	if *css.ContentType != "text/css; charset=utf-8" {
		t.Fatalf("unexpected style.css content type: %s", *css.ContentType)
	}

	js, ok := byKey["script.js"]
	if !ok {
		t.Fatal("expected an upload with key script.js")
	}
	if *js.ContentType != "application/javascript; charset=utf-8" {
		t.Fatalf("unexpected script.js content type: %s", *js.ContentType)
	}
}

func TestNewClientConfiguresCorrectEndpoint(t *testing.T) {
	client, err := NewClient(context.Background(), "acct123", "key", "secret")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	opts := client.Options()
	if opts.Region != "auto" {
		t.Fatalf("unexpected region: %s", opts.Region)
	}
	if opts.BaseEndpoint == nil || *opts.BaseEndpoint != "https://acct123.r2.cloudflarestorage.com" {
		t.Fatalf("unexpected base endpoint: %v", opts.BaseEndpoint)
	}
}
