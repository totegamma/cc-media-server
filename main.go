package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"log"
	"net/url"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/disintegration/imaging"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/totegamma/concurrent/x/auth"
	"github.com/totegamma/concurrent/x/core"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var (
	bucketName      = ""
	region          = ""
	endpointUrl     = ""
	accessKeyId     = ""
	accessKeySecret = ""
	publicBaseUrl   = ""
	forcePathStyle  = bool(false)
	quota           = int64(0)
	db_dsn          = ""
)

func stripExif(data *bytes.Reader) (*bytes.Reader, error) {
	_, _, err := image.DecodeConfig(data)
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
	region = os.Getenv("region")
	accessKeyId = os.Getenv("accessKeyId")
	accessKeySecret = os.Getenv("accessKeySecret")
	publicBaseUrl = os.Getenv("publicBaseUrl")
	forcePathStyle, _ = strconv.ParseBool(os.Getenv("forcePathStyle"))
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
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKeyId, accessKeySecret, "")),
	)

	if err != nil {
		log.Fatal(err)
	}

	client := s3.NewFromConfig(cfg, func(options *s3.Options) {
		options.UsePathStyle = forcePathStyle
	})

	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORS())
	e.Use(auth.ReceiveGatewayAuthPropagation)

	e.GET("/user", func(c echo.Context) error {
		requester, ok := c.Get(core.RequesterIdCtxKey).(string)
		if !ok {
			return c.JSON(400, echo.Map{"error": "invalid requester"})
		}

		var user StorageUser
		err = db.Where("id = ?", requester).First(&user).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		return c.JSON(200, user)
	})

	e.POST("/files", func(c echo.Context) error {
		body := c.Request().Body

		requester, ok := c.Get(core.RequesterIdCtxKey).(string)
		if !ok {
			return c.JSON(400, echo.Map{"error": "invalid requester"})
		}

		var user StorageUser
		err = db.FirstOrCreate(&user, StorageUser{ID: requester}).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		buf, err := io.ReadAll(body)
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		reader, err := stripExif(bytes.NewReader(buf))
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}
		size := reader.Size()

		if user.TotalBytes+size > quota {
			return c.JSON(403, echo.Map{"error": "quota exceeded"})
		}

		fileID, err := uploadFile(client, requester, reader, size)
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
			URL:     publicBaseUrl + requester + "/" + fileID,
			OwnerID: requester,
			Size:    size,
		}).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		return c.JSON(200, echo.Map{"status": "ok", "content": file})
	})

	e.GET("/files", func(c echo.Context) error {
		requester, ok := c.Get(core.RequesterIdCtxKey).(string)
		if !ok {
			return c.JSON(400, echo.Map{"error": "invalid requester"})
		}

		afterStr := c.QueryParam("after")
		beforeStr := c.QueryParam("before")

		limitStr := c.QueryParam("limit")
		limit, err := strconv.Atoi(limitStr)
		if err != nil {
			limit = 20
		}
		if limit > 100 {
			limit = 100
		}

		var files []StorageFile
		var next string
		var prev string
		if afterStr != "" { // prev
			afterInt, err := strconv.ParseInt(afterStr, 10, 64)
			if err != nil {
				return c.JSON(400, echo.Map{"error": "invalid after"})
			}
			after := time.Unix(afterInt, 0)
			err = db.Where("owner_id = ? AND c_date > ?", requester, after).Order("c_date asc").Limit(limit + 1).Find(&files).Error
			if err != nil {
				log.Println(err)
				return c.JSON(500, err)
			}

			next = strconv.FormatInt(files[0].CDate.Unix(), 10)
			if len(files) > limit {
				prev = strconv.FormatInt(files[limit-2].CDate.Unix(), 10)
				files = files[:limit]
			}

			slices.Reverse(files)

		} else if beforeStr != "" { // next
			beforeInt, err := strconv.ParseInt(beforeStr, 10, 64)
			if err != nil {
				return c.JSON(400, echo.Map{"error": "invalid before"})
			}
			before := time.Unix(beforeInt, 0)
			err = db.Where("owner_id = ? AND c_date < ?", requester, before).Order("c_date desc").Limit(limit + 1).Find(&files).Error
			if err != nil {
				log.Println(err)
				return c.JSON(500, err)
			}

			prev = strconv.FormatInt(files[0].CDate.Unix(), 10)
			if len(files) > limit {
				next = strconv.FormatInt(files[limit-2].CDate.Unix(), 10)
				files = files[:limit]
			}

		} else { // beforeのうち、最新のものを取得
			err = db.Where("owner_id = ?", requester).Order("c_date desc").Limit(limit + 1).Find(&files).Error
			if err != nil {
				log.Println(err)
				return c.JSON(500, err)
			}

			if len(files) > limit {
				next = strconv.FormatInt(files[limit-2].CDate.Unix(), 10)
				files = files[:limit]
			}
		}

		result := FilesResponse{
			Status:  "ok",
			Content: files,
			Next:    next,
			Prev:    prev,
			Limit:   limit,
		}

		return c.JSON(200, result)

	})

	e.DELETE("/file/:id", func(c echo.Context) error {

		requester, ok := c.Get(core.RequesterIdCtxKey).(string)
		if !ok {
			return c.JSON(400, echo.Map{"error": "invalid requester"})
		}

		id := c.Param("id")

		var file StorageFile
		err = db.Where("id = ?", id).First(&file).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		if file.OwnerID != requester {
			return c.JSON(403, echo.Map{"error": "you are not owner"})
		}

		err = deleteFile(client, requester+"/"+id)
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
		err = db.Where("id = ?", requester).First(&user).Error
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
