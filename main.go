package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/totegamma/concrnt-playground"
	"github.com/totegamma/concrnt-playground/impl/interop"

	_ "github.com/joho/godotenv/autoload"
)

var (
	bucketName      = ""
	region          = ""
	endpointUrl     = ""
	accessKeyId     = ""
	accessKeySecret = ""
	publicBaseUrl   = ""
	forcePathStyle  = bool(false)
	defaultQuota    = int64(0)
	db_dsn          = ""
	port            = ""
)

var (
	version      = "unknown"
	buildMachine = "unknown"
	buildTime    = "unknown"
	goVersion    = "unknown"
)

func main() {

	fmt.Printf("cc-media-server version: %s, buildMachine: %s, buildTime: %s, goVersion: %s\n", version, buildMachine, buildTime, goVersion)

	bucketName = os.Getenv("bucketName")
	endpointUrl = os.Getenv("endpointUrl")
	region = os.Getenv("region")
	accessKeyId = os.Getenv("accessKeyId")
	accessKeySecret = os.Getenv("accessKeySecret")
	publicBaseUrl = os.Getenv("publicBaseUrl")
	forcePathStyle, _ = strconv.ParseBool(os.Getenv("forcePathStyle"))
	defaultQuota, _ = strconv.ParseInt(os.Getenv("quota"), 10, 64)
	db_dsn = os.Getenv("db_dsn")
	port = os.Getenv("port")

	db, err := gorm.Open(postgres.Open(db_dsn), &gorm.Config{})
	if err != nil {
		panic("failed to connect database")
	}

	err = db.AutoMigrate(&StorageUser{}, &StorageFile{})
	if err != nil {
		panic("failed to migrate database")
	}

	log.Println("bucketName: ", bucketName)
	log.Println("defaultQuota: ", defaultQuota)

	resolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...any) (aws.Endpoint, error) {
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
	presignClient := s3.NewPresignClient(client)

	e := echo.New()
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORS())
	e.Use(interop.ReceiveGatewayAuthPropagation)

	e.GET("/cc-info", func(c echo.Context) error {
		return c.JSON(http.StatusOK, interop.CCInfo{
			Name:    "github.com/totegamma/cc-media-server",
			Version: version,
			Endpoints: map[string]concrnt.ConcrntEndpoint{
				"net.concrnt.storage.user":    {Template: "/user", Method: http.MethodGet},
				"net.concrnt.storage.upload":  {Template: "/files", Method: http.MethodPost},
				"net.concrnt.storage.presign": {Template: "/presign", Method: http.MethodPost},
				"net.concrnt.storage.list":    {Template: "/files", Method: http.MethodGet, Query: &[]string{"after", "before", "limit"}},
				"net.concrnt.storage.delete":  {Template: "/file/{id}", Method: http.MethodDelete},
				"net.concrnt.storage.resolve": {Template: "/resolve/{hash}", Method: http.MethodGet},
			},
		})
	})

	// ユーザー情報の取得
	e.GET("/user", func(c echo.Context) error {
		ctx := c.Request().Context()

		requester := ctx.Value(interop.RequesterCtxKey).(concrnt.Entity)

		quota := defaultQuota

		var user StorageUser
		err = db.WithContext(ctx).Where("id = ?", requester.CCID).First(&user).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		user.Quota = quota

		return c.JSON(200, echo.Map{"status": "ok", "content": user})
	})

	// ファイルのアップロード
	e.POST("/files", func(c echo.Context) error {
		ctx := c.Request().Context()

		body := c.Request().Body
		header := c.Request().Header

		requester := ctx.Value(interop.RequesterCtxKey).(concrnt.Entity)

		contentType := header.Get("Content-Type")

		quota := defaultQuota
		/*
			requesterTag, ok := ctx.Value(core.RequesterTagCtxKey).(core.Tags)
			if ok {
				value, ok := requesterTag.GetAsInt("mediaServerQuota")
				if ok {
					quota = int64(value)
				}
			}
		*/

		var user StorageUser
		err = db.WithContext(ctx).FirstOrCreate(&user, StorageUser{ID: requester.CCID}).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		buf, err := io.ReadAll(body)
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		hash := sha256.Sum256(buf)

		reader := bytes.NewReader(buf)
		size := reader.Size()

		if user.TotalBytes+size > quota {
			return c.JSON(403, echo.Map{"error": "quota exceeded"})
		}

		fileID, extension, err := uploadFile(ctx, client, requester.CCID, reader, size, contentType)
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
			Sha256:  hex.EncodeToString(hash[:]),
			URL:     path.Join(publicBaseUrl, requester.CCID, fileID+extension),
			OwnerID: requester.CCID,
			Size:    size,
			Mime:    contentType,
		}).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		return c.JSON(200, echo.Map{"status": "ok", "content": file})
	})

	// 署名付きURLの発行
	e.POST("/presign", func(c echo.Context) error {
		ctx := c.Request().Context()

		requester := ctx.Value(interop.RequesterCtxKey).(concrnt.Entity)

		quota := defaultQuota
		/*
			requesterTag, ok := ctx.Value(core.RequesterTagCtxKey).(core.Tags)
			if ok {
				value, ok := requesterTag.GetAsInt("mediaServerQuota")
				if ok {
					quota = int64(value)
				}
			}
		*/

		var req struct {
			ContentType string `json:"contentType"`
			Size        int64  `json:"size"`
			Sha256      string `json:"sha256"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(400, echo.Map{"error": "invalid request body"})
		}

		if req.Size <= 0 {
			return c.JSON(400, echo.Map{"error": "size must be greater than 0"})
		}

		if req.Sha256 == "" {
			return c.JSON(400, echo.Map{"error": "sha256 is required"})
		}

		if req.ContentType == "" {
			req.ContentType = "application/octet-stream"
		}

		fileID, extension, key := makeObjectKey(requester.CCID, req.ContentType)

		expiresIn := 10 * time.Minute
		presigned, err := presignClient.PresignPutObject(
			ctx,
			&s3.PutObjectInput{
				Bucket:            &bucketName,
				Key:               aws.String(key),
				ContentType:       aws.String(req.ContentType),
				ContentLength:     aws.Int64(req.Size),
				ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
				ChecksumSHA256:    aws.String(req.Sha256),
			},
			s3.WithPresignExpires(expiresIn),
		)
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		headers := map[string]string{}
		for k, values := range presigned.SignedHeader {
			if len(values) > 0 {
				headers[k] = values[0]
			}
		}

		var file StorageFile
		err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var user StorageUser
			if err := tx.FirstOrCreate(&user, StorageUser{ID: requester.CCID}).Error; err != nil {
				return err
			}

			if user.TotalBytes+req.Size > quota {
				return echo.NewHTTPError(403, "quota exceeded")
			}

			user.TotalBytes += req.Size
			if err := tx.Save(&user).Error; err != nil {
				return err
			}

			file = StorageFile{
				ID:      fileID,
				Sha256:  req.Sha256,
				URL:     publicBaseUrl + requester.CCID + "/" + fileID + extension,
				OwnerID: requester.CCID,
				Size:    req.Size,
				Mime:    req.ContentType,
			}
			if err := tx.Create(&file).Error; err != nil {
				return err
			}

			return nil
		})
		if err != nil {
			var httpErr *echo.HTTPError
			if errors.As(err, &httpErr) {
				return c.JSON(httpErr.Code, echo.Map{"error": httpErr.Message})
			}
			log.Println(err)
			return c.JSON(500, err)
		}

		return c.JSON(200, echo.Map{
			"status": "ok",
			"content": echo.Map{
				"file":      file,
				"key":       key,
				"url":       presigned.URL,
				"method":    http.MethodPut,
				"headers":   headers,
				"expiresIn": int(expiresIn.Seconds()),
			},
		})
	})

	// ファイルの一覧取得
	e.GET("/files", func(c echo.Context) error {
		ctx := c.Request().Context()

		requester := ctx.Value(interop.RequesterCtxKey).(concrnt.Entity)

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
			err = db.WithContext(ctx).Where("owner_id = ? AND c_date > ?", requester.CCID, after).Order("c_date asc").Limit(limit + 1).Find(&files).Error
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
			err = db.WithContext(ctx).Where("owner_id = ? AND c_date < ?", requester.CCID, before).Order("c_date desc").Limit(limit + 1).Find(&files).Error
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
			err = db.WithContext(ctx).Where("owner_id = ?", requester.CCID).Order("c_date desc").Limit(limit + 1).Find(&files).Error
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

	// ファイルの削除
	e.DELETE("/file/:id", func(c echo.Context) error {
		ctx := c.Request().Context()

		requester := ctx.Value(interop.RequesterCtxKey).(concrnt.Entity)

		id := c.Param("id")

		var file StorageFile
		err = db.WithContext(ctx).Where("id = ?", id).First(&file).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		if file.OwnerID != requester.CCID {
			return c.JSON(403, echo.Map{"error": "you are not owner"})
		}

		key := strings.TrimPrefix(file.URL, publicBaseUrl)
		err = deleteFile(ctx, client, key)
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		err = db.WithContext(ctx).Delete(&file).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		var user StorageUser
		err = db.WithContext(ctx).Where("id = ?", requester.CCID).First(&user).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}
		user.TotalBytes -= file.Size
		err = db.WithContext(ctx).Save(&user).Error
		if err != nil {
			log.Println(err)
			return c.JSON(500, err)
		}

		return c.JSON(200, echo.Map{"status": "ok"})
	})

	e.GET("/resolve/:hash", func(c echo.Context) error {
		ctx := c.Request().Context()
		hash := c.Param("hash")

		var file StorageFile
		err = db.WithContext(ctx).Where("sha256 = ?", hash).First(&file).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return c.JSON(404, echo.Map{"error": "file not found"})
			}
			log.Println(err)
			return c.JSON(500, err)
		}

		c.Response().Header().Set("Location", file.URL)
		return c.JSON(301, echo.Map{"status": "ok", "content": file})
	})

	portStr := ":8000"
	if port != "" {
		portStr = ":" + port
	}

	panic(e.Start(portStr))
}
