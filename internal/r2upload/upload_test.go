package r2upload

import (
	"bytes"
	"context"
	"io"
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

func TestVideoUploaderUsesMP4KeyAndContentType(t *testing.T) {
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

func TestPageUploaderUsesPPrefixKeyAndHTMLContentType(t *testing.T) {
	fake := &fakePutObjectAPI{}
	p := PageUploader{Client: fake, Bucket: "bucket", PublicBaseURL: "https://videos.example.com"}
	url, err := p.PublishPage("999", "<html></html>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *fake.gotInput.Key != "p/999.html" {
		t.Fatalf("unexpected key: %s", *fake.gotInput.Key)
	}
	if *fake.gotInput.ContentType != "text/html; charset=utf-8" {
		t.Fatalf("unexpected content type: %s", *fake.gotInput.ContentType)
	}
	if url != "https://videos.example.com/p/999.html" {
		t.Fatalf("unexpected url: %s", url)
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
