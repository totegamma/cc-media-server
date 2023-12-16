package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var (
	bucketName      = ""
	endpointUrl     = ""
	accessKeyId     = ""
	accessKeySecret = ""
	publicBaseUrl   = ""
	quota           = int64(0)
	db_dsn          = ""
)

func uploadFile(client *s3.Client, userID string, data io.Reader, length int64) (string, error) {

	id := uuid.New().String()
	key := fmt.Sprintf("%s/%s", userID, id)

	_, err := client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket:        &bucketName,
		Key:           aws.String(key),
		Body:          data,
		ContentType:   aws.String("image/jpeg"),
		ContentLength: aws.Int64(length),
	})

	if err != nil {
		log.Println(err)
		return "", err
	}

	log.Println("Uploaded to: ", publicBaseUrl+url.PathEscape(key))

	return id, nil
}

func deleteFile(client *s3.Client, key string) error {
	_, err := client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
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

func main() {

	bucketName = os.Getenv("bucketName")
	endpointUrl = os.Getenv("endpointUrl")
	accessKeySecret = os.Getenv("accessKeySecret")
	publicBaseUrl = os.Getenv("publicBaseUrl")
	quota, _ = strconv.ParseInt(os.Getenv("quota"), 10, 64)
	db_dsn = os.Getenv("db_dsn")

	db, err := gorm.Open(postgres.Open(db_dsn), &gorm.Config{})
	if err != nil {
		panic("failed to connect database")
	}

	db.AutoMigrate(&StorageUser{}, &StorageFile{})

	log.Println("bucketName: ", bucketName)
	log.Println("quota: ", quota)

	resolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		return aws.Endpoint{URL: endpointUrl}, nil
	})

	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithEndpointResolverWithOptions(resolver),
		config.WithRegion("auto"),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyId, accessKeySecret, "")),
	)
	if err != nil {
		log.Fatal(err)
	}

	client := s3.NewFromConfig(cfg)

	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORS())

	e.GET("/user", func(c echo.Context) error {
		userID := c.Request().Header.Get("cc-user-id")
		if len(userID) != 42 {
			return c.JSON(400, echo.Map{"error": "invalid cc-user-id"})
		}

		var user StorageUser
		err = db.Where("id = ?", userID).First(&user).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		return c.JSON(200, user)
	})

	e.POST("/files", func(c echo.Context) error {
		body := c.Request().Body

		userID := c.Request().Header.Get("cc-user-id")
		if len(userID) != 42 {
			return c.JSON(400, echo.Map{"error": "invalid cc-user-id"})
		}

		var user StorageUser
		err = db.FirstOrCreate(&user, StorageUser{ID: userID}).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		buf, err := io.ReadAll(body)
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		reader := bytes.NewReader(buf)
		size := int64(len(buf))

		if user.TotalBytes+size > quota {
			return c.JSON(403, echo.Map{"error": "quota exceeded"})
		}

		fileID, err := uploadFile(client, userID, reader, size)
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		user.TotalBytes += size
		err = db.Save(&user).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		var file StorageFile
		err = db.FirstOrCreate(&file, StorageFile{
			ID:      fileID,
			URL:     publicBaseUrl + userID + "/" + fileID,
			OwnerID: userID,
			Size:    size,
		}).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		return c.JSON(200, echo.Map{"status": "ok", "content": file})
	})

	e.GET("/files", func(c echo.Context) error {
		userID := c.Request().Header.Get("cc-user-id")
		if len(userID) != 42 {
			return c.JSON(400, echo.Map{"error": "invalid cc-user-id"})
		}

		var files []StorageFile
		err = db.Where("owner_id = ?", userID).Find(&files).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		return c.JSON(200, files)
	})

	e.DELETE("/file/:id", func(c echo.Context) error {
		userID := c.Request().Header.Get("cc-user-id")
		if len(userID) != 42 {
			return c.JSON(400, echo.Map{"error": "invalid cc-user-id"})
		}

		id := c.Param("id")

		var file StorageFile
		err = db.Where("id = ?", id).First(&file).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		if file.OwnerID != userID {
			return c.JSON(403, echo.Map{"error": "you are not owner"})
		}

		err = deleteFile(client, userID+"/"+id)
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		err = db.Delete(&file).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		var user StorageUser
		err = db.Where("id = ?", userID).First(&user).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}
		user.TotalBytes -= file.Size
		err = db.Save(&user).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		return c.JSON(200, echo.Map{"status": "ok"})
	})

	e.Start(":8000")
}
