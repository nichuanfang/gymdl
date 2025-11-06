package utils

import (
	"encoding/json"
	"fmt"
)

type QQFileInfo struct {
	SizeFlac   int64   `json:"size_flac"`
	Size192Ogg int64   `json:"size_192ogg"`
	Size96Ogg  int64   `json:"size_96ogg"`
	Size320Mp3 int64   `json:"size_320mp3"`
	Size128Mp3 int64   `json:"size_128mp3"`
	Size192Aac int64   `json:"size_192aac"`
	Size96Aac  int64   `json:"size_96aac"`
	Size48Aac  int64   `json:"size_48aac"`
	SizeNew    []int64 `json:"size_new"`
}

type QQSongFileType string

const (
	FLAC    QQSongFileType = "FLAC"
	OGG_640 QQSongFileType = "OGG_640"
	OGG_320 QQSongFileType = "OGG_320"
	OGG_192 QQSongFileType = "OGG_192"
	OGG_96  QQSongFileType = "OGG_96"
	MP3_320 QQSongFileType = "MP3_320"
	MP3_128 QQSongFileType = "MP3_128"
	ACC_192 QQSongFileType = "ACC_192"
	ACC_96  QQSongFileType = "ACC_96"
	ACC_48  QQSongFileType = "ACC_48"
)

// 文件后缀映射表
var fileExtensionMap = map[QQSongFileType]string{
	FLAC:    ".flac",
	OGG_640: ".ogg",
	OGG_320: ".ogg",
	OGG_192: ".ogg",
	OGG_96:  ".ogg",
	MP3_320: ".mp3",
	MP3_128: ".mp3",
	ACC_192: ".m4a",
	ACC_96:  ".m4a",
	ACC_48:  ".m4a",
}

// GetBestQuality 返回最高可用音质类型和对应文件后缀
func GetBestQuality(data []byte) (QQSongFileType, string, error) {
	file := &QQFileInfo{}
	if err := json.Unmarshal(data, file); err != nil {
		return "", "", err
	}

	exists := map[QQSongFileType]int64{
		FLAC:    file.SizeFlac,
		OGG_640: getArrayValue(file.SizeNew, 5),
		OGG_320: getArrayValue(file.SizeNew, 3),
		OGG_192: file.Size192Ogg,
		MP3_320: file.Size320Mp3,
		ACC_192: file.Size192Aac,
		OGG_96:  file.Size96Ogg,
		MP3_128: file.Size128Mp3,
		ACC_96:  file.Size96Aac,
		ACC_48:  file.Size48Aac,
	}

	order := []QQSongFileType{
		FLAC, OGG_640, OGG_320, OGG_192,
		MP3_320, ACC_192, OGG_96, MP3_128,
		ACC_96, ACC_48,
	}

	for _, t := range order {
		if exists[t] > 0 {
			ext := fileExtensionMap[t]
			return t, ext, nil
		}
	}

	return "", "", fmt.Errorf("未找到可用音质")
}

func getArrayValue(arr []int64, index int) int64 {
	if index >= 0 && index < len(arr) {
		return arr[index]
	}
	return 0
}
