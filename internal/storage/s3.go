package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/draas-platform/draas/internal/backup"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"io"
	"net/http"
	"time"
)

type S3 struct {
	Client *minio.Client
	Bucket string
}

func New(endpoint, access, secret, bucket string, secure bool) (*S3, error) {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 30 * time.Second
	t.MaxIdleConnsPerHost = 16
	c, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(access, secret, ""), Secure: secure, Transport: t})
	if err != nil {
		return nil, err
	}
	return &S3{Client: c, Bucket: bucket}, nil
}
func (s *S3) Ensure(ctx context.Context) error {
	exists, err := s.Client.BucketExists(ctx, s.Bucket)
	if err != nil {
		return err
	}
	if !exists {
		return s.Client.MakeBucket(ctx, s.Bucket, minio.MakeBucketOptions{})
	}
	return nil
}
func (s *S3) PutIfAbsent(ctx context.Context, key string, b []byte) (bool, error) {
	opts := minio.PutObjectOptions{ContentType: "application/octet-stream", DisableMultipart: true}
	opts.SetMatchETagExcept("*")
	_, err := s.Client.PutObject(ctx, s.Bucket, key, bytes.NewReader(b), int64(len(b)), opts)
	if err != nil {
		r := minio.ToErrorResponse(err)
		if r.StatusCode == 412 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
func (s *S3) Get(ctx context.Context, key string, max int64) ([]byte, error) {
	obj, err := s.Client.GetObject(ctx, s.Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	b, err := io.ReadAll(io.LimitReader(obj, max+1))
	if err != nil {
		r := minio.ToErrorResponse(err)
		if r.Code == "NoSuchKey" || r.StatusCode == 404 {
			return nil, backup.ErrNotFound
		}
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("object exceeds %d bytes", max)
	}
	return b, nil
}
func (s *S3) List(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	for o := range s.Client.ListObjects(ctx, s.Bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		keys = append(keys, o.Key)
		if len(keys) > 100000 {
			return nil, errors.New("catalog rebuild limit exceeded; partition prefix required")
		}
	}
	return keys, ctx.Err()
}
