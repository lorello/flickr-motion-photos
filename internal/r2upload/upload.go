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

type VideoUploader struct {
	Client        PutObjectAPI
	Bucket        string
	PublicBaseURL string
}

func (u VideoUploader) UploadVideo(photoID string, videoBytes []byte) (string, error) {
	return PutObject(context.Background(), u.Client, u.Bucket, u.PublicBaseURL, photoID+".mp4", "video/mp4", videoBytes)
}

type PageUploader struct {
	Client        PutObjectAPI
	Bucket        string
	PublicBaseURL string
}

func (p PageUploader) PublishPage(photoID, html string) (string, error) {
	return PutObject(context.Background(), p.Client, p.Bucket, p.PublicBaseURL, "p/"+photoID+".html", "text/html; charset=utf-8", []byte(html))
}
