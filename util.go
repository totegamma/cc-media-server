package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/disintegration/imaging"
	"github.com/google/uuid"
	"image"
	"io"
	"log"
	"mime"
	"net/url"
)

func uploadFile(ctx context.Context, client *s3.Client, userID string, data io.Reader, length int64, contentType string) (string, string, error) {
	id := uuid.New().String()

	if contentType == "" {
		contentType = "application/octet-stream"
	}

	extension := ""
	extensions, err := mime.ExtensionsByType(contentType)
	if err == nil {
		extension = extensions[0]
		if extension == ".jpe" { // workaround for jpeg
			extension = ".jpeg"
		}
	}

	key := userID + "/" + id + extension

	_, err = client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        &bucketName,
		Key:           aws.String(key),
		Body:          data,
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(length),
	})

	if err != nil {
		log.Println(err)
		return "", "", err
	}

	log.Println("Uploaded to: ", publicBaseUrl+url.PathEscape(key))

	return id, extension, nil
}

func deleteFile(ctx context.Context, client *s3.Client, key string) error {
	_, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: &bucketName,
		Key:    aws.String(key),
	})

	if err != nil {
		log.Println(err)
		return err
	}

	log.Println("Deleted: ", key)

	return nil
}

func stripExif(data *bytes.Reader) (*bytes.Reader, error) {
	_, _, err := image.DecodeConfig(data)
	defer data.Seek(0, io.SeekStart)

	if err != nil {
		if errors.Is(err, image.ErrFormat) {
			// not a JPEG image, no need to strip EXIF
			return data, nil
		}
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}
	_, _ = data.Seek(0, io.SeekStart)

	// this strips EXIF data away from the JPEG image
	img, err := imaging.Decode(data, imaging.AutoOrientation(true))
	if err != nil {
		return nil, fmt.Errorf("failed to decode image: %w", err)
	}

	var buf bytes.Buffer
	if err := imaging.Encode(&buf, img, imaging.JPEG, imaging.JPEGQuality(75)); err != nil {
		return nil, fmt.Errorf("failed to re-encode Exif-stripped JPEG image: %w", err)
	}
	return bytes.NewReader(buf.Bytes()), nil
}
