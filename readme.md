# cc-media-server
Concurrent Media Server

## Development

### Install
```bash
go mod download
```

### DB
```bash
docker-compose up -d db
```

### Run
```bash
go build -o mediaserver
./mediaserver
```

## Environment Variables
table of environment variables
All environment variables are required.

| Variable        | Description                  | Sample                                                                                     |
|-----------------|------------------------------|--------------------------------------------------------------------------------------------|
| db_dsn          | Database connection string   | host=localhost user=postgres password=postgres dbname=concurrent port=5432 sslmode=disable |
| quota           | Quota for the storage (Byte) | 100000                                                                                     |
| bucketName      | Name of the bucket           | xxxx                                                                                       |
| endpointUrl     | Endpoint URL                 | https://xxxxxx.r2.cloudflarestorage.com                                                    |
| region          | Region                       | auto                                                                                       |
| accessKeyId     | Access Key ID                |                                                                                            |
| accessKeySecret | Access Key Secret            |                                                                                            |
| publicBaseUrl   | Public base URL              | https://storage.example.com/                                                               |
