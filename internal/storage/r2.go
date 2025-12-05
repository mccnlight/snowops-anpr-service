package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

var ErrNotConfigured = errors.New("r2 storage is not configured")

type R2Client struct {
	client        *s3.Client
	bucket        string
	endpoint      string
	publicBaseURL string
}

type r2Config struct {
	Endpoint      string
	AccessKey     string
	SecretKey     string
	Bucket        string
	Region        string
	PublicBaseURL string
}

func NewR2ClientFromEnv() (*R2Client, error) {
	cfg := r2Config{
		Endpoint:      strings.TrimSpace(os.Getenv("R2_ENDPOINT")),
		AccessKey:     strings.TrimSpace(os.Getenv("R2_ACCESS_KEY_ID")),
		SecretKey:     strings.TrimSpace(os.Getenv("R2_SECRET_ACCESS_KEY")),
		Bucket:        strings.TrimSpace(os.Getenv("R2_BUCKET")),
		Region:        strings.TrimSpace(os.Getenv("R2_REGION")),
		PublicBaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("R2_PUBLIC_BASE_URL")), "/"),
	}

	if cfg.Endpoint == "" || cfg.AccessKey == "" || cfg.SecretKey == "" || cfg.Bucket == "" {
		return nil, ErrNotConfigured
	}
	if cfg.Region == "" {
		cfg.Region = "auto"
	}

	resolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		if service == s3.ServiceID {
			return aws.Endpoint{
				URL:               cfg.Endpoint,
				HostnameImmutable: true,
			}, nil
		}
		return aws.Endpoint{}, &aws.EndpointNotFoundError{}
	})

	awsCfg := aws.Config{
		Region:                      cfg.Region,
		Credentials:                 credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
		EndpointResolverWithOptions: resolver,
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = true
	})

	return &R2Client{
		client:        client,
		bucket:        cfg.Bucket,
		endpoint:      strings.TrimRight(cfg.Endpoint, "/"),
		publicBaseURL: cfg.PublicBaseURL,
	}, nil
}

func (r *R2Client) Upload(ctx context.Context, key string, body io.Reader, size int64, contentType string) (string, error) {
	if r == nil || r.client == nil {
		return "", ErrNotConfigured
	}
	if size <= 0 {
		return "", fmt.Errorf("empty file")
	}
	input := &s3.PutObjectInput{
		Bucket:      &r.bucket,
		Key:         &key,
		Body:        body,
		ContentType: aws.String(contentType),
		ContentLength: func() *int64 {
			if size < 0 {
				return nil
			}
			return aws.Int64(size)
		}(),
	}
	if _, err := r.client.PutObject(ctx, input); err != nil {
		return "", fmt.Errorf("r2 upload failed: %w", err)
	}
	return r.objectURL(key), nil
}

func (r *R2Client) objectURL(key string) string {
	trimmedKey := strings.TrimLeft(key, "/")
	if r.publicBaseURL != "" {
		return fmt.Sprintf("%s/%s/%s", r.publicBaseURL, r.bucket, trimmedKey)
	}
	return fmt.Sprintf("%s/%s/%s", r.endpoint, r.bucket, trimmedKey)
}
