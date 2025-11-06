package utils

import (
	"fmt"
	"strings"
)

// QQFileInfo QQ 音乐接口返回的文件大小信息
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

// QQMusicFileMetadata 返回的文件元信息
type QQMusicFileMetadata struct {
	Quality   string // 音质类型（最高可用）
	FileExt   string // 文件扩展名(带 .)
	Ext       string //扩展(不带 .)
	MusicSize int64  // 文件大小（字节）
	Bitrate   int    // 比特率（kbps，无单位）
}

// QQSongFileType 音乐文件类型定义
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

// GetBestQuality 根据 QQ 音乐文件信息返回最高可用音质
func ParseQQFileMetadate(file QQFileInfo, interval int) (QQMusicFileMetadata, error) {

	// 各音质文件存在性与大小映射
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

	// 音质优先级排序
	order := []QQSongFileType{
		FLAC, OGG_640, OGG_320, OGG_192,
		MP3_320, ACC_192, OGG_96, MP3_128,
		ACC_96, ACC_48,
	}

	for _, t := range order {
		size := exists[t]
		if size > 0 {
			ext := fileExtensionMap[t]
			bitrate := calculateBitrate(size, interval)
			return QQMusicFileMetadata{
				Quality:   string(t),
				FileExt:   ext,
				Ext:       strings.TrimPrefix(ext, "."),
				MusicSize: size,
				Bitrate:   bitrate,
			}, nil
		}
	}

	return QQMusicFileMetadata{}, fmt.Errorf("未找到可用音质")
}

// getArrayValue 安全读取数组元素
func getArrayValue(arr []int64, index int) int64 {
	if index >= 0 && index < len(arr) {
		return arr[index]
	}
	return 0
}

// calculateBitrate 根据文件大小与时长计算比特率（kbps）
func calculateBitrate(sizeBytes int64, durationSec int) int {
	if durationSec <= 0 || sizeBytes <= 0 {
		return 0
	}
	// sizeBytes * 8 / durationSec / 1000
	return int((sizeBytes * 8) / int64(durationSec) / 1000)
}
