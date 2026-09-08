// Package r2upload uploads objects (extracted videos, generated viewer
// pages) to a Cloudflare R2 bucket over its S3-compatible API. Both videos
// and HTML pages go through the same bucket/client — one storage backend,
// no separate hosting service.
package r2upload

import (
	"bytes"
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// PutObjectAPI is the subset of the S3 client PutObject needs, so tests can
// substitute a fake.
type PutObjectAPI interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

func NewClient(ctx context.Context, accountID, accessKeyID, secretAccessKey string) (*s3.Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("auto"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, "")),
	)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID)
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
	}), nil
}

// PutObject uploads data to key in bucket (overwrite, naturally idempotent)
// and returns the public URL under publicBaseURL.
func PutObject(ctx context.Context, client PutObjectAPI, bucket, publicBaseURL, key, contentType string, data []byte) (string, error) {
	_, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(data),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s", publicBaseURL, key), nil
}

// VideoUploader/PageUploader adapt PutObject to the scanner's Uploader/
// Publisher interfaces (see internal/scanner).

// VideoUploader keeps a flat "<id>.mp4" key deliberately — it's never
// user-facing (only referenced from inside the generated page's <video
// src>), and every video already published this session lives at that flat
// path; adding a username prefix here would silently orphan them all
// without a bucket copy/migration this project doesn't do.
type VideoUploader struct {
	Client        PutObjectAPI
	Bucket        string
	PublicBaseURL string
}

func (u VideoUploader) UploadVideo(photoID string, videoBytes []byte) (string, error) {
	return PutObject(context.Background(), u.Client, u.Bucket, u.PublicBaseURL, photoID+".mp4", "video/mp4", videoBytes)
}

// PageUploader is user-facing (linked from the Flickr description), so its
// key mirrors Flickr's own URL scheme (photos/<username>/<id>) — unique
// today since Flickr photo IDs are globally unique regardless of owner,
// and organizes the bucket per-user ahead of any future multi-account use.
type PageUploader struct {
	Client        PutObjectAPI
	Bucket        string
	PublicBaseURL string
	Username      string
}

func (p PageUploader) PublishPage(photoID, html string) (string, error) {
	key := fmt.Sprintf("photos/%s/%s.html", p.Username, photoID)
	return PutObject(context.Background(), p.Client, p.Bucket, p.PublicBaseURL, key, "text/html; charset=utf-8", []byte(html))
}
