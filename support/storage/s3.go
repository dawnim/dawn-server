package storage

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/prismgo/framework/filesystem"
)

// S3 适配AWS、七牛和COS的兼容接口；签名、重试和流式传输由SDK处理。
type S3 struct {
	client    *s3.Client
	transport *http.Transport
	cfg       Config
}

func NewS3(cfg Config) *S3 {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	client := s3.New(s3.Options{Region: cfg.Region, Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""), HTTPClient: &http.Client{Transport: transport, Timeout: time.Minute}, UsePathStyle: cfg.PathStyle, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired}, func(o *s3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})
	return &S3{client: client, transport: transport, cfg: cfg}
}
func (d *S3) key(key string) string {
	if prefix := strings.Trim(d.cfg.Prefix, "/"); prefix != "" {
		return prefix + "/" + strings.TrimLeft(key, "/")
	}
	return strings.TrimLeft(key, "/")
}
func (d *S3) Close() error { d.transport.CloseIdleConnections(); return nil }
func (d *S3) Write(ctx context.Context, key string, reader io.Reader, opts filesystem.PutOptions) error {
	input := &s3.PutObjectInput{Bucket: aws.String(d.cfg.Bucket), Key: aws.String(d.key(key)), Body: reader, ContentType: aws.String(opts.ContentType)}
	if d.cfg.ACL != "" {
		input.ACL = types.ObjectCannedACL(d.cfg.ACL)
	}
	_, err := d.client.PutObject(ctx, input)
	return err
}
func (d *S3) Open(ctx context.Context, key string) (io.ReadCloser, filesystem.FileInfo, error) {
	r, err := d.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(d.cfg.Bucket), Key: aws.String(d.key(key))})
	if err != nil {
		return nil, filesystem.FileInfo{}, err
	}
	return r.Body, filesystem.FileInfo{Path: key, Size: aws.ToInt64(r.ContentLength), ContentType: aws.ToString(r.ContentType), LastModified: aws.ToTime(r.LastModified)}, nil
}
func (d *S3) ReadAll(ctx context.Context, key string) ([]byte, error) {
	r, _, err := d.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
func (d *S3) Stat(ctx context.Context, key string) (filesystem.FileInfo, error) {
	r, err := d.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(d.cfg.Bucket), Key: aws.String(d.key(key))})
	if err != nil {
		return filesystem.FileInfo{}, err
	}
	return filesystem.FileInfo{Path: key, Size: aws.ToInt64(r.ContentLength), ContentType: aws.ToString(r.ContentType), LastModified: aws.ToTime(r.LastModified)}, nil
}
func (d *S3) Exists(ctx context.Context, key string) (bool, error) {
	_, err := d.Stat(ctx, key)
	var api smithy.APIError
	if errors.As(err, &api) && (api.ErrorCode() == "NotFound" || api.ErrorCode() == "NoSuchKey") {
		return false, nil
	}
	return err == nil, err
}
func (d *S3) Delete(ctx context.Context, key string) error {
	_, err := d.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(d.cfg.Bucket), Key: aws.String(d.key(key))})
	return err
}
func (d *S3) Copy(ctx context.Context, src, dst string) error {
	_, err := d.client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: aws.String(d.cfg.Bucket), Key: aws.String(d.key(dst)), CopySource: aws.String(url.PathEscape(d.cfg.Bucket + "/" + d.key(src)))})
	return err
}
func (d *S3) Move(ctx context.Context, src, dst string) error {
	if err := d.Copy(ctx, src, dst); err != nil {
		return err
	}
	return d.Delete(ctx, src)
}
func (d *S3) List(ctx context.Context, prefix string, recursive bool) ([]filesystem.FileInfo, error) {
	input := &s3.ListObjectsV2Input{Bucket: aws.String(d.cfg.Bucket), Prefix: aws.String(d.key(prefix))}
	if !recursive {
		input.Delimiter = aws.String("/")
	}
	pager := s3.NewListObjectsV2Paginator(d.client, input)
	files := []filesystem.FileInfo{}
	for pager.HasMorePages() {
		r, err := pager.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range r.Contents {
			files = append(files, filesystem.FileInfo{Path: strings.TrimPrefix(aws.ToString(o.Key), strings.Trim(d.cfg.Prefix, "/")+"/"), Size: aws.ToInt64(o.Size), LastModified: aws.ToTime(o.LastModified)})
		}
		for _, p := range r.CommonPrefixes {
			files = append(files, filesystem.FileInfo{Path: aws.ToString(p.Prefix), IsDir: true})
		}
	}
	return files, nil
}
func (d *S3) MakeDirectory(context.Context, string) error { return nil }
func (d *S3) DeleteDirectory(ctx context.Context, dir string) error {
	files, err := d.List(ctx, strings.TrimRight(dir, "/")+"/", true)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := d.Delete(ctx, f.Path); err != nil {
			return err
		}
	}
	return nil
}
func (d *S3) Path(key string) string { return "s3://" + d.cfg.Bucket + "/" + d.key(key) }
func (d *S3) URL(key string) (string, error) {
	if d.cfg.URL == "" {
		return "", filesystem.ErrPublicURLUnavailable
	}
	return strings.TrimRight(d.cfg.URL, "/") + "/" + d.key(key), nil
}
func (d *S3) TemporaryURL(ctx context.Context, key string, expiry time.Time) (string, error) {
	r, err := s3.NewPresignClient(d.client).PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(d.cfg.Bucket), Key: aws.String(d.key(key))}, s3.WithPresignExpires(time.Until(expiry)))
	if err != nil {
		return "", err
	}
	return r.URL, nil
}
func (d *S3) SetVisibility(ctx context.Context, key, visibility string) error {
	acl := types.ObjectCannedACLPrivate
	if visibility == "public" {
		acl = types.ObjectCannedACLPublicRead
	}
	_, err := d.client.PutObjectAcl(ctx, &s3.PutObjectAclInput{Bucket: aws.String(d.cfg.Bucket), Key: aws.String(d.key(key)), ACL: acl})
	return err
}
func (d *S3) GetVisibility(ctx context.Context, key string) (string, error) {
	r, err := d.client.GetObjectAcl(ctx, &s3.GetObjectAclInput{Bucket: aws.String(d.cfg.Bucket), Key: aws.String(d.key(key))})
	if err != nil {
		return "", err
	}
	for _, g := range r.Grants {
		if g.Grantee != nil && aws.ToString(g.Grantee.URI) == "http://acs.amazonaws.com/groups/global/AllUsers" {
			return "public", nil
		}
	}
	return "private", nil
}

var _ filesystem.Driver = (*S3)(nil)
