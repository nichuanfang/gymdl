package utils

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/disintegration/imaging"
	"github.com/nichuanfang/gymdl/internal/musiccover"
)

var client = &http.Client{
	Timeout: 10 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
	},
}

// FetchImage 下载图片
func FetchImage(url string) ([]byte, error) {
	// 发起请求
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %s", resp.Status)
	}

	// 限制最大读取大小，防止内存攻击
	const maxImageSize = 10 << 20
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageSize))
	if err != nil {
		return nil, err
	}

	return ResizeMusicCover(data)
}

// ResizeMusicCover encodes an image as a square JPEG for audio tags.
func ResizeMusicCover(data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	resized := imaging.Fill(img, musiccover.Size, musiccover.Size, imaging.Center, imaging.Lanczos)

	// 编码为 JPEG 格式，压缩质量设为85以平衡性能与质量
	buf := new(bytes.Buffer)
	err = jpeg.Encode(buf, resized, &jpeg.Options{Quality: 85})
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// DownloadAndSaveImage 从指定 URL 下载图片，并保存为 JPEG 文件到 path
func DownloadAndSaveImage(url, path string) error {
	// 调用之前的 FetchImage 获取图片字节
	data, err := FetchImage(url)
	if err != nil {
		return err
	}

	// 创建文件
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	// 写入数据到文件
	_, err = file.Write(data)
	if err != nil {
		return err
	}

	return nil
}
