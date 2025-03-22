package main

import (
	"context"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
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
	if err == nil && len(extensions) > 0 {
		extension = extensions[len(extensions)-1]
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
