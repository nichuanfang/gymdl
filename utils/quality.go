package utils

import (
	"encoding/json"
	"fmt"
)

type SongFileType string

const (
	FLAC    SongFileType = "FLAC"    // FLAC格式, 16-24Bit, size_flac
	OGG_640 SongFileType = "OGG_640" // OGG 640kbps, size_new[5]
	OGG_320 SongFileType = "OGG_320" // OGG 320kbps, size_new[3]
	OGG_192 SongFileType = "OGG_192" // OGG 192kbps, size_192ogg
	OGG_96  SongFileType = "OGG_96"  // OGG 96kbps, size_96ogg
	MP3_320 SongFileType = "MP3_320" // MP3 320kbps, size_320mp3
	MP3_128 SongFileType = "MP3_128" // MP3 128kbps, size_128mp3
	ACC_192 SongFileType = "ACC_192" // M4A 192kbps, size_192aac
	ACC_96  SongFileType = "ACC_96"  // M4A 96kbps, size_96aac
	ACC_48  SongFileType = "ACC_48"  // M4A 48kbps, size_48aac
)

func GetBestQuality(data []byte) (SongFileType, error) {
	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return "", err
	}

	dataArr, ok := root["data"].([]interface{})
	if !ok || len(dataArr) == 0 {
		return "", fmt.Errorf("响应中没有 data 数组")
	}

	first, ok := dataArr[0].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("data[0] 不是对象")
	}

	file, ok := first["file"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("缺少 file 字段")
	}

	sizeNew := func(index int) int64 {
		if arr, ok := file["size_new"].([]interface{}); ok && index < len(arr) {
			if f, ok := arr[index].(float64); ok {
				return int64(f)
			}
		}
		return 0
	}

	getSize := func(key string) int64 {
		if v, ok := file[key]; ok {
			if f, ok := v.(float64); ok {
				return int64(f)
			}
		}
		return 0
	}

	exists := map[SongFileType]int64{
		FLAC:    getSize("size_flac"),
		OGG_320: sizeNew(3),
		OGG_640: sizeNew(5),
		OGG_192: getSize("size_192ogg"),
		OGG_96:  getSize("size_96ogg"),
		MP3_320: getSize("size_320mp3"),
		MP3_128: getSize("size_128mp3"),
		ACC_192: getSize("size_192aac"),
		ACC_96:  getSize("size_96aac"),
		ACC_48:  getSize("size_48aac"),
	}

	// 音质优先级（从高到低）
	order := []SongFileType{
		FLAC,
		OGG_640,
		OGG_320,
		OGG_192,
		MP3_320,
		ACC_192,
		OGG_96,
		MP3_128,
		ACC_96,
		ACC_48,
	}

	for _, t := range order {
		if exists[t] > 0 {
			return t, nil
		}
	}

	return "", fmt.Errorf("未找到可用音质")
}
