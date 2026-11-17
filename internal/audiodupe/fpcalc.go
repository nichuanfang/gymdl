package audiodupe

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// ParseFpcalc reads fpcalc -json -raw output. Text output is accepted as a fallback.
func ParseFpcalc(output []byte) (Fingerprint, error) {
	output = bytes.TrimSpace(output)
	if len(output) == 0 {
		return Fingerprint{}, fmt.Errorf("fpcalc 没有输出")
	}
	if output[0] == '{' {
		var payload struct {
			Duration    float64 `json:"duration"`
			Fingerprint []int64 `json:"fingerprint"`
		}
		if err := json.Unmarshal(output, &payload); err != nil {
			return Fingerprint{}, fmt.Errorf("fpcalc 输出无法解析")
		}
		if payload.Duration <= 0 || len(payload.Fingerprint) == 0 {
			return Fingerprint{}, fmt.Errorf("fpcalc 没有算出指纹")
		}
		items := make([]uint32, len(payload.Fingerprint))
		for i, item := range payload.Fingerprint {
			items[i] = uint32(item)
		}
		return Fingerprint{Duration: payload.Duration, Items: items}, nil
	}
	var duration float64
	var raw string
	for _, line := range strings.Split(string(output), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "DURATION":
			parsed, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return Fingerprint{}, fmt.Errorf("fpcalc 时长无效")
			}
			duration = parsed
		case "FINGERPRINT":
			raw = value
		}
	}
	if duration <= 0 || raw == "" {
		return Fingerprint{}, fmt.Errorf("fpcalc 没有算出指纹")
	}
	parts := strings.Split(raw, ",")
	items := make([]uint32, 0, len(parts))
	for _, part := range parts {
		parsed, err := strconv.ParseUint(strings.TrimSpace(part), 10, 32)
		if err != nil {
			return Fingerprint{}, fmt.Errorf("fpcalc 指纹无效")
		}
		items = append(items, uint32(parsed))
	}
	return Fingerprint{Duration: duration, Items: items}, nil
}

// Command builds the fpcalc invocation used for a whole local file.
func Command(binary, path string) *exec.Cmd {
	return exec.Command(binary, "-json", "-raw", path)
}
