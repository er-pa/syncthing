package blob

import (
	"bytes"
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"
)

type S3Config struct {
	Bucket    string
	Endpoint  string
	Region    string
	AccessKey string
	SecretKey string
}

func (s *S3Config) isSet() bool {
	return s.AccessKey != "" && s.SecretKey != "" && s.Bucket != "" && s.Endpoint != ""
}

type S3 struct {
	client *s3.S3
	bucket string
}

func NewS3(config S3Config) (*S3, error) {
	s3Config := &aws.Config{
		Credentials:      credentials.NewStaticCredentials(config.AccessKey, config.SecretKey, ""),
		Endpoint:         aws.String(fmt.Sprintf("https://%s.%s", config.Bucket, config.Endpoint)),
		Region:           aws.String(config.Region),
		S3ForcePathStyle: aws.Bool(false),
	}
	newSession, err := session.NewSession(s3Config)
	if err != nil {
		return nil, err
	}
	s3Client := s3.New(newSession)

	return &S3{client: s3Client, bucket: config.Bucket}, nil
}

func (s *S3) Put(key string, data []byte) error {
	uploader := s3manager.NewUploaderWithClient(s.client)

	// Upload the file.
	_, err := uploader.Upload(&s3manager.UploadInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	return err
}

func (s *S3) Get(key string) ([]byte, error) {
	downloader := s3manager.NewDownloaderWithClient(s.client)
	buf := aws.NewWriteAtBuffer([]byte{})

	// Download the file.
	_, err := downloader.Download(buf, &s3.GetObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (s *S3) Delete(key string) error {
	// Delete the item.
	_, err := s.client.DeleteObject(&s3.DeleteObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(key)})
	if err != nil {
		return err
	}

	// Wait until the object is deleted.
	err = s.client.WaitUntilObjectNotExists(&s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})

	return err
}

func (s *S3) Iterate(key string, fn func([]byte) bool) error {
	// Obtain the list of objects with a certain prefix.
	resp, err := s.client.ListObjectsV2(&s3.ListObjectsV2Input{Bucket: &s.bucket, Prefix: aws.String(key)})
	if err != nil {
		return err
	}

	// Download the actual content of each obtained object.
	for _, item := range resp.Contents {
		b, err := s.Get(*item.Key)
		if err != nil {
			continue
		}

		if !fn(b) {
			break
		}

	}
	return nil
}
