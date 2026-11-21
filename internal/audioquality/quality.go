package audioquality

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type Result struct {
	Title, Artist, Album string
	Source               string
	Codec                string
	Bitrate              int
	Duration             float64
	Decoded              float64
	Reference            float64
	Damaged              bool
	Unreadable           bool
}

func LowBitrate(codec string, bitrate int) bool {
	return bitrate > 0 && bitrate < bitrateFloor(codec)
}

func bitrateFloor(codec string) int {
	switch codec {
	case "mp3":
		return 192
	case "aac", "vorbis":
		return 128
	case "opus":
		return 96
	default:
		return 0
	}
}

func (r Result) ShorterThanSource() bool {
	if r.Reference <= 0 || r.Duration <= 0 {
		return false
	}
	actual := r.Duration
	if r.Decoded > 0 {
		actual = r.Decoded
	}
	return r.Reference-actual >= math.Max(15, r.Reference*0.15)
}

func (r Result) NeedsReview() bool {
	return r.Damaged || r.ShorterThanSource() || LowBitrate(r.Codec, r.Bitrate)
}

const audioFormats = "aac,asf,flac,m4a,mov,mp3,mp4,ogg,wav"

// Inspect reads just the selected audio stream. Decode errors on readable
// files indicate damage; timeouts and tool failures can be retried.
func Inspect(ctx context.Context, filename string) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	probe := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe",
		"-format_whitelist", audioFormats, "-select_streams", "a:0",
		"-show_entries", "stream=codec_name,bit_rate,duration:stream_tags=title,artist,album,comment:format=duration:format_tags=title,artist,album,comment",
		"-of", "json", "-i", filename)
	output, err := probe.Output()
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return Result{}, err
		}
		var status *exec.ExitError
		if !errors.As(err, &status) || status.ExitCode() < 0 {
			return Result{}, err
		}
		return Result{Damaged: true}, nil
	}
	type audioStream struct {
		Codec    string `json:"codec_name"`
		Bitrate  string `json:"bit_rate"`
		Duration string
		Tags     map[string]string
	}
	var parsed struct {
		Streams []audioStream
		Format  audioStream
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		return Result{}, fmt.Errorf("解析音轨失败: %w", err)
	}
	if len(parsed.Streams) == 0 {
		return Result{Damaged: true}, nil
	}
	stream := parsed.Streams[0]
	tag := func(name string) string {
		for _, tags := range []map[string]string{stream.Tags, parsed.Format.Tags} {
			for key, value := range tags {
				if strings.EqualFold(key, name) && strings.TrimSpace(value) != "" {
					return strings.TrimSpace(value)
				}
			}
		}
		return ""
	}
	bps, _ := strconv.Atoi(stream.Bitrate)
	declared := seconds(stream.Duration)
	if declared == 0 {
		declared = seconds(parsed.Format.Duration)
	}
	result := Result{
		Title: tag("title"), Artist: tag("artist"), Album: tag("album"), Source: tag("comment"),
		Codec: stream.Codec, Bitrate: (bps + 500) / 1000, Duration: declared,
	}
	var progress bytes.Buffer
	decode := exec.CommandContext(ctx, "ffmpeg", "-nostdin", "-v", "error", "-xerror", "-err_detect", "explode",
		"-protocol_whitelist", "file,pipe", "-format_whitelist", audioFormats, "-threads", "1",
		"-i", filename, "-map", "0:a:0", "-nostats", "-progress", "pipe:1", "-f", "null", "-")
	decode.Stdout = &progress
	decodeErr := decode.Run()
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	if decodeErr != nil {
		var status *exec.ExitError
		if !errors.As(decodeErr, &status) || status.ExitCode() < 0 {
			return Result{}, decodeErr
		}
		result.Damaged = true
	}
	for _, line := range strings.Split(progress.String(), "\n") {
		if value, ok := strings.CutPrefix(line, "out_time_us="); ok {
			result.Decoded = max(result.Decoded, seconds(strings.TrimSpace(value))/1e6)
		}
	}
	if result.Duration > 0 && result.Decoded > 0 &&
		result.Duration-result.Decoded >= math.Max(2, result.Duration*0.05) {
		result.Damaged = true
	}
	if result.Bitrate == 0 && !result.Damaged && result.Decoded > 0 && bitrateFloor(result.Codec) > 0 {
		// Opus and other variable-rate streams may omit stream.bit_rate.
		// Use actual audio packet bytes, excluding tags and embedded artwork.
		packets := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe",
			"-format_whitelist", audioFormats, "-select_streams", "a:0", "-show_packets",
			"-show_entries", "packet=size", "-of", "csv=p=0", "-i", filename)
		if data, err := packets.Output(); err == nil {
			var bytes int64
			for _, line := range strings.Split(string(data), "\n") {
				size, _ := strconv.ParseInt(strings.TrimSpace(strings.SplitN(line, ",", 2)[0]), 10, 64)
				bytes += max(size, 0)
			}
			result.Bitrate = int(math.Round(float64(bytes) * 8 / (result.Decoded * 1000)))
		} else if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
	}
	return result, nil
}

func seconds(raw string) float64 {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}
