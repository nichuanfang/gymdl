package utils

import (
	"encoding/json"
	"testing"
)

func TestGetBestQuality(t *testing.T) {
	tests := []struct {
		name     string
		root     Root
		wantType SongFileType
		wantErr  bool
	}{
		{
			name: "优先返回 FLAC",
			root: Root{
				Data: []SongData{
					{File: FileInfo{SizeFlac: 12345, Size320Mp3: 9999}},
				},
			},
			wantType: FLAC,
			wantErr:  false,
		},
		{
			name: "没有 FLAC，返回 OGG_640",
			root: Root{
				Data: []SongData{
					{File: FileInfo{SizeNew: []int64{0, 0, 0, 0, 0, 8888}}},
				},
			},
			wantType: OGG_640,
			wantErr:  false,
		},
		{
			name: "只有 MP3_128",
			root: Root{
				Data: []SongData{
					{File: FileInfo{Size128Mp3: 5555}},
				},
			},
			wantType: MP3_128,
			wantErr:  false,
		},
		{
			name: "空 data 数组",
			root: Root{
				Data: []SongData{},
			},
			wantType: "",
			wantErr:  true,
		},
		{
			name: "无可用音质",
			root: Root{
				Data: []SongData{
					{File: FileInfo{}},
				},
			},
			wantType: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, _ := json.Marshal(tt.root)
			gotType, err := GetBestQuality(data)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetBestQuality() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotType != tt.wantType {
				t.Errorf("GetBestQuality() got = %v, want %v", gotType, tt.wantType)
			}
		})
	}
}
