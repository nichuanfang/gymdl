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
	SizeDolby  int64   `json:"size_dolby"`
	SizeNew    []int64 `json:"size_new"`
}

type QQMusicFileMetadata struct {
	Quality   string
	FileType  int
	FileExt   string
	Ext       string
	MusicSize int64
	Bitrate   int
}

type QQFileType int

const (
	DTS_X              QQFileType = 0
	MASTER             QQFileType = 1
	ATMOS_2            QQFileType = 2
	ATMOS_51           QQFileType = 3
	ATMOS_71           QQFileType = 4
	ATMOS_DB           QQFileType = 5
	NAC                QQFileType = 6
	FLAC               QQFileType = 7
	OGG_640            QQFileType = 8
	OGG_320            QQFileType = 9
	OGG_192            QQFileType = 10
	OGG_96             QQFileType = 11
	MP3_320            QQFileType = 12
	MP3_128            QQFileType = 13
	AAC_192            QQFileType = 14
	AAC_96             QQFileType = 15
	AAC_48             QQFileType = 16
	ENCRYPTED_DTS_X    QQFileType = 17
	ENCRYPTED_VINYL    QQFileType = 18
	ENCRYPTED_MASTER   QQFileType = 19
	ENCRYPTED_ATMOS_2  QQFileType = 20
	ENCRYPTED_ATMOS_51 QQFileType = 21
	ENCRYPTED_ATMOS_71 QQFileType = 22
	ENCRYPTED_ATMOS_DB QQFileType = 23
	ENCRYPTED_NAC      QQFileType = 24
	ENCRYPTED_FLAC     QQFileType = 25
	ENCRYPTED_OGG_640  QQFileType = 26
	ENCRYPTED_OGG_320  QQFileType = 27
	ENCRYPTED_OGG_192  QQFileType = 28
	ENCRYPTED_OGG_96   QQFileType = 29
)

// 全局静态优先级队列
var qualityOrder = [...]QQFileType{
	MASTER, ATMOS_71, ATMOS_51, ATMOS_2, ATMOS_DB, DTS_X,
	FLAC, OGG_640, MP3_320, OGG_320, AAC_192, OGG_192, MP3_128, AAC_96, AAC_48, OGG_96,

	ENCRYPTED_MASTER, ENCRYPTED_ATMOS_71, ENCRYPTED_ATMOS_51, ENCRYPTED_ATMOS_2, ENCRYPTED_ATMOS_DB, ENCRYPTED_DTS_X, ENCRYPTED_VINYL,
	ENCRYPTED_FLAC, ENCRYPTED_OGG_640, ENCRYPTED_OGG_320, ENCRYPTED_OGG_192, ENCRYPTED_OGG_96,
}

var fileTypeStringArray = [...]string{
	DTS_X: "DTS_X", MASTER: "MASTER", ATMOS_2: "ATMOS_2", ATMOS_51: "ATMOS_51", ATMOS_71: "ATMOS_71", ATMOS_DB: "ATMOS_DB", NAC: "NAC", FLAC: "FLAC", OGG_640: "OGG_640", OGG_320: "OGG_320", OGG_192: "OGG_192", OGG_96: "OGG_96", MP3_320: "MP3_320", MP3_128: "MP3_128", AAC_192: "AAC_192", AAC_96: "AAC_96", AAC_48: "AAC_48",
	ENCRYPTED_DTS_X: "ENCRYPTED_DTS_X", ENCRYPTED_VINYL: "ENCRYPTED_VINYL", ENCRYPTED_MASTER: "ENCRYPTED_MASTER", ENCRYPTED_ATMOS_2: "ENCRYPTED_ATMOS_2", ENCRYPTED_ATMOS_51: "ENCRYPTED_ATMOS_51", ENCRYPTED_ATMOS_71: "ENCRYPTED_ATMOS_71", ENCRYPTED_ATMOS_DB: "ENCRYPTED_ATMOS_DB", ENCRYPTED_NAC: "ENCRYPTED_NAC", ENCRYPTED_FLAC: "ENCRYPTED_FLAC", ENCRYPTED_OGG_640: "ENCRYPTED_OGG_640", ENCRYPTED_OGG_320: "ENCRYPTED_OGG_320", ENCRYPTED_OGG_192: "ENCRYPTED_OGG_192", ENCRYPTED_OGG_96: "ENCRYPTED_OGG_96",
}

var fileExtArray = [...]string{
	DTS_X: ".flac", MASTER: ".flac", ATMOS_2: ".flac", ATMOS_51: ".flac", ATMOS_71: ".flac", ATMOS_DB: ".m4a", NAC: ".flac", FLAC: ".flac", OGG_640: ".ogg", OGG_320: ".ogg", OGG_192: ".ogg", OGG_96: ".ogg", MP3_320: ".mp3", MP3_128: ".mp3", AAC_192: ".m4a", AAC_96: ".m4a", AAC_48: ".m4a",
	ENCRYPTED_DTS_X: ".flac", ENCRYPTED_VINYL: ".flac", ENCRYPTED_MASTER: ".flac", ENCRYPTED_ATMOS_2: ".flac", ENCRYPTED_ATMOS_51: ".flac", ENCRYPTED_ATMOS_71: ".flac", ENCRYPTED_ATMOS_DB: ".m4a", ENCRYPTED_NAC: ".flac", ENCRYPTED_FLAC: ".flac", ENCRYPTED_OGG_640: ".ogg", ENCRYPTED_OGG_320: ".ogg", ENCRYPTED_OGG_192: ".ogg", ENCRYPTED_OGG_96: ".ogg",
}

var extArray = [...]string{
	DTS_X: "flac", MASTER: "flac", ATMOS_2: "flac", ATMOS_51: "flac", ATMOS_71: "flac", ATMOS_DB: "m4a", NAC: "flac", FLAC: "flac", OGG_640: "ogg", OGG_320: "ogg", OGG_192: "ogg", OGG_96: "ogg", MP3_320: "mp3", MP3_128: "mp3", AAC_192: "m4a", AAC_96: "m4a", AAC_48: "m4a",
	ENCRYPTED_DTS_X: "flac", ENCRYPTED_VINYL: "flac", ENCRYPTED_MASTER: "flac", ENCRYPTED_ATMOS_2: "flac", ENCRYPTED_ATMOS_51: "flac", ENCRYPTED_ATMOS_71: "flac", ENCRYPTED_ATMOS_DB: "m4a", ENCRYPTED_NAC: "flac", ENCRYPTED_FLAC: "flac", ENCRYPTED_OGG_640: "ogg", ENCRYPTED_OGG_320: "ogg", ENCRYPTED_OGG_192: "ogg", ENCRYPTED_OGG_96: "ogg",
}

// ParseQQFileMetadate 新增 vipLevel 参数 控制最高可用音质返回
// vipLevel 可传入: "vip" (普通豪华绿钻) 或 "svip" (超级会员)
func ParseQQFileMetadate(file *QQFileInfo, interval int, vipLevel string) (QQMusicFileMetadata, error) {
	if file == nil {
		return QQMusicFileMetadata{}, fmt.Errorf("file is nil")
	}

	// 统一转成小写，防止大小写输入不一致
	level := strings.ToLower(vipLevel)
	isSVIP := level == "svip"

	for _, t := range qualityOrder {
		// 如果不是 SVIP，且当前音质是 SVIP 专属音质，直接跳过不解析
		if !isSVIP && isSVIPOnlyQuality(t) {
			continue
		}

		size := getFileSizeLazy(file, t)
		if size > 0 {
			idx := int(t)
			if idx >= len(fileTypeStringArray) {
				continue
			}

			return QQMusicFileMetadata{
				Quality:   fileTypeStringArray[idx],
				FileType:  idx,
				FileExt:   fileExtArray[idx],
				Ext:       extArray[idx],
				MusicSize: size,
				Bitrate:   calculateBitrate(size, interval),
			}, nil
		}
	}

	return QQMusicFileMetadata{}, fmt.Errorf("未找到可用音质")
}

// isSVIPOnlyQuality 判定该音质是否为 SVIP 专属
func isSVIPOnlyQuality(t QQFileType) bool {
	switch t {
	case MASTER, ENCRYPTED_MASTER, // 臻品母带
		ATMOS_2, ENCRYPTED_ATMOS_2, // 臻品音质
		ATMOS_51, ENCRYPTED_ATMOS_51, // 臻品全景声 5.1
		ATMOS_71, ENCRYPTED_ATMOS_71, // 臻品全景声 7.1
		ATMOS_DB, ENCRYPTED_ATMOS_DB, // 杜比全景声
		DTS_X, ENCRYPTED_DTS_X, // DTS:X
		ENCRYPTED_VINYL: // 黑胶
		return true
	default:
		return false
	}
}

func getFileSizeLazy(file *QQFileInfo, t QQFileType) int64 {
	switch t {
	case MASTER, ENCRYPTED_MASTER:
		return getArrayValue(file.SizeNew, 0)
	case ATMOS_2, ENCRYPTED_ATMOS_2:
		return getArrayValue(file.SizeNew, 1)
	case ATMOS_51, ENCRYPTED_ATMOS_51:
		return getArrayValue(file.SizeNew, 2)
	case OGG_320, ENCRYPTED_OGG_320:
		return getArrayValue(file.SizeNew, 3)
	case ENCRYPTED_VINYL:
		return getArrayValue(file.SizeNew, 4)
	case OGG_640, ENCRYPTED_OGG_640:
		return getArrayValue(file.SizeNew, 5)
	case ATMOS_71, ENCRYPTED_ATMOS_71:
		return getArrayValue(file.SizeNew, 6)
	case DTS_X, ENCRYPTED_DTS_X:
		return getArrayValue(file.SizeNew, 9)
	case ATMOS_DB, ENCRYPTED_ATMOS_DB:
		return file.SizeDolby
	case FLAC, ENCRYPTED_FLAC:
		return file.SizeFlac
	case OGG_192, ENCRYPTED_OGG_192:
		return file.Size192Ogg
	case OGG_96, ENCRYPTED_OGG_96:
		return file.Size96Ogg
	case MP3_320:
		return file.Size320Mp3
	case MP3_128:
		return file.Size128Mp3
	case AAC_192:
		return file.Size192Aac
	case AAC_96:
		return file.Size96Aac
	case AAC_48:
		return file.Size48Aac
	default:
		return 0
	}
}

func getArrayValue(arr []int64, index int) int64 {
	if index >= 0 && index < len(arr) {
		return arr[index]
	}
	return 0
}

func calculateBitrate(sizeBytes int64, durationSec int) int {
	if durationSec <= 0 || sizeBytes <= 0 {
		return 0
	}
	return int((sizeBytes * 8) / int64(durationSec) / 1000)
}
