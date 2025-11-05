package utils

import (
    "encoding/json"
    "fmt"
)

type FileInfo struct {
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

type SongData struct {
	File FileInfo `json:"file"`
}

type Root struct {
	Data []SongData `json:"data"`
}

type SongFileType string

const (
	FLAC    SongFileType = "FLAC"
	OGG_640 SongFileType = "OGG_640"
	OGG_320 SongFileType = "OGG_320"
	OGG_192 SongFileType = "OGG_192"
	OGG_96  SongFileType = "OGG_96"
	MP3_320 SongFileType = "MP3_320"
	MP3_128 SongFileType = "MP3_128"
	ACC_192 SongFileType = "ACC_192"
	ACC_96  SongFileType = "ACC_96"
	ACC_48  SongFileType = "ACC_48"
)

func GetBestQuality(data []byte) (SongFileType, error) {
	var root Root
	if err := json.Unmarshal(data, &root); err != nil {
		return "", err
	}

	if len(root.Data) == 0 {
		return "", fmt.Errorf("响应中没有 data 数组")
	}

	file := root.Data[0].File

	exists := map[SongFileType]int64{
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

	order := []SongFileType{
		FLAC, OGG_640, OGG_320, OGG_192,
		MP3_320, ACC_192, OGG_96, MP3_128,
		ACC_96, ACC_48,
	}

	for _, t := range order {
		if exists[t] > 0 {
			return t, nil
		}
	}

	return "", fmt.Errorf("未找到可用音质")
}

func getArrayValue(arr []int64, index int) int64 {
	if index >= 0 && index < len(arr) {
		return arr[index]
	}
	return 0
}