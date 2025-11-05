package _struct

type QuerySongResponse struct {
	Code      int      `json:"code"`
	Message   string   `json:"message"`
	Data      []QQSong `json:"data"`
	Timestamp int64    `json:"timestamp"`
}

type QQSong struct {
	ID          int         `json:"id"`
	Type        int         `json:"type"`
	Mid         string      `json:"mid"`
	Name        string      `json:"name"`
	Title       string      `json:"title"`
	Subtitle    string      `json:"subtitle"`
	Singer      interface{} `json:"singer"`
	Album       interface{} `json:"album"`
	MV          interface{} `json:"mv"`
	Interval    int         `json:"interval"`
	IsOnly      int         `json:"isonly"`
	Language    int         `json:"language"`
	Genre       int         `json:"genre"`
	IndexCD     int         `json:"index_cd"`
	IndexAlbum  int         `json:"index_album"`
	TimePublic  string      `json:"time_public"`
	Status      int         `json:"status"`
	Fnote       int         `json:"fnote"`
	File        QQFile      `json:"file"`
	Pay         interface{} `json:"pay"`
	Action      interface{} `json:"action"`
	Va          []any       `json:"va"`
	Ksong       interface{} `json:"ksong"`
	Volume      interface{} `json:"volume"`
	Label       string      `json:"label"`
	URL         string      `json:"url"`
	Ppurl       string      `json:"ppurl"`
	Bpm         int         `json:"bpm"`
	Version     int         `json:"version"`
	Trace       string      `json:"trace"`
	DataType    int         `json:"data_type"`
	ModifyStamp int         `json:"modify_stamp"`
	Aid         int         `json:"aid"`
	Tid         int         `json:"tid"`
	Ov          int         `json:"ov"`
	Sa          int         `json:"sa"`
	Es          string      `json:"es"`
	Vs          []string    `json:"vs"`
	Vf          []float64   `json:"vf"`
	Vi          []int       `json:"vi"`
}

type QQFile struct {
	MediaMid      string `json:"media_mid"`
	Size24aac     int    `json:"size_24aac"`
	Size48aac     int    `json:"size_48aac"`
	Size96aac     int    `json:"size_96aac"`
	Size192aac    int    `json:"size_192aac"`
	Size192ogg    int    `json:"size_192ogg"`
	Size128mp3    int    `json:"size_128mp3"`
	Size320mp3    int    `json:"size_320mp3"`
	SizeApe       int    `json:"size_ape"`
	SizeFlac      int    `json:"size_flac"`
	SizeDts       int    `json:"size_dts"`
	SizeTry       int    `json:"size_try"`
	TryBegin      int    `json:"try_begin"`
	TryEnd        int    `json:"try_end"`
	URL           string `json:"url"`
	SizeHires     int    `json:"size_hires"`
	HiresSample   int    `json:"hires_sample"`
	HiresBitdepth int    `json:"hires_bitdepth"`
	B30s          int    `json:"b_30s"`
	E30s          int    `json:"e_30s"`
	Size96ogg     int    `json:"size_96ogg"`
	Size360ra     []any  `json:"size_360ra"` // 如果确定是数字可以改成 []int
	SizeDolby     int    `json:"size_dolby"`
	SizeNew       []int  `json:"size_new"` // 数组元素是整数
}
