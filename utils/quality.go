package utils

import (
	"encoding/json"
	"fmt"
)

type SongFileType string

const (
	FLAC    SongFileType = "SongFileType.FLAC"
	OGG_640 SongFileType = "SongFileType.OGG_640"
	OGG_320 SongFileType = "SongFileType.OGG_320"
	MP3_320 SongFileType = "SongFileType.MP3_320"
	ACC_192 SongFileType = "SongFileType.ACC_192"
	OGG_192 SongFileType = "SongFileType.OGG_192"
	MP3_128 SongFileType = "SongFileType.MP3_128"
	ACC_96  SongFileType = "SongFileType.ACC_96"
	OGG_96  SongFileType = "SongFileType.OGG_96"
	ACC_48  SongFileType = "SongFileType.ACC_48"
)

func GetBestQualityFromJSON(data []byte) (SongFileType, error) {
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

	getInt := func(key string) int {
		if v, ok := file[key]; ok {
			if f, ok := v.(float64); ok {
				return int(f)
			}
		}
		return 0
	}

	exists := map[SongFileType]bool{
		FLAC:    getInt("size_flac") > 0,
		OGG_640: getInt("size_192ogg") > 0,
		OGG_320: getInt("size_192ogg") > 0,
		MP3_320: getInt("size_320mp3") > 0,
		ACC_192: getInt("size_192aac") > 0,
		OGG_192: getInt("size_192ogg") > 0,
		MP3_128: getInt("size_128mp3") > 0,
		ACC_96:  getInt("size_96aac") > 0,
		OGG_96:  getInt("size_96ogg") > 0,
		ACC_48:  getInt("size_48aac") > 0,
	}

	// 按行业音质优先级
	order := []SongFileType{
		FLAC, OGG_640, OGG_320, MP3_320,
		ACC_192, OGG_192, MP3_128,
		ACC_96, OGG_96, ACC_48,
	}

	for _, t := range order {
		if exists[t] {
			return t, nil
		}
	}
	return "", fmt.Errorf("未找到可用音质")
}
