package main

import (
	"time"
)

type StorageUser struct {
	ID         string    `json:"id" gorm:"primaryKey;type:char(42)"`
	TotalBytes int64     `json:"totalBytes" gorm:"type:bigint"`
	CDate      time.Time `json:"cdate" gorm:"->;<-:create;autoCreateTime"`
	MDate      time.Time `json:"mdate" gorm:"autoUpdateTime"`
}

type StorageFile struct {
	ID      string    `json:"id" gorm:"primaryKey;type:uuid;"`
	URL     string    `json:"url" gorm:"type:text"`
	OwnerID string    `json:"ownerId" gorm:"type:char(42)"`
	Size    int64     `json:"size" gorm:"type:bigint"`
	CDate   time.Time `json:"cdate" gorm:"->;<-:create;autoCreateTime"`
}

type FilesResponse struct {
	Status  string        `json:"status"`
	Content []StorageFile `json:"content"`
	Limit   int           `json:"limit,omitempty"`
	Next    string        `json:"next,omitempty"`
	Prev    string        `json:"prev,omitempty"`
}
