package music

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/nichuanfang/gymdl/utils"
)

const (
	ytDLPMaxCapturedLines = 8
	ytDLPMaxCapturedLine  = 2048
	ytDLPMaxErrorDetail   = 300
)

type ytDLPFailure struct {
	message string
	cause   error
}

func (e *ytDLPFailure) Error() string { return e.message }
func (e *ytDLPFailure) Unwrap() error { return e.cause }

func newYTDLPFailure(stage string, cause error, output string) error {
	if strings.Contains(strings.ToLower(output), "requested format is not available") {
		return &ytDLPFailure{
			message: stage + "：当前下载设置下没有可用的音频格式。请确认视频可访问、Cookie 有效，并检查 yt-dlp 格式设置后重试。",
			cause:   cause,
		}
	}

	if detail := extractYTDLPErrorDetail(output); detail != "" {
		return &ytDLPFailure{
			message: stage + "：" + utils.TruncateString(detail, ytDLPMaxErrorDetail),
			cause:   cause,
		}
	}

	if cause != nil {
		return &ytDLPFailure{
			message: fmt.Sprintf("%s：进程执行失败（%v），未获取到 yt-dlp 的详细错误输出。", stage, cause),
			cause:   cause,
		}
	}

	return &ytDLPFailure{message: stage + "：未获取到 yt-dlp 的错误详情。"}
}

func extractYTDLPErrorDetail(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		upperLine := strings.ToUpper(line)
		if index := strings.Index(upperLine, "ERROR:"); index >= 0 {
			return strings.TrimSpace(line[index+len("ERROR:"):])
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

func runYTDLPCommand(cmd *exec.Cmd) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("读取 yt-dlp 标准输出失败：%w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("读取 yt-dlp 错误输出失败：%w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 yt-dlp 失败，请确认已安装且位于 PATH 中：%w", err)
	}

	var stderrOutput string
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = readYTDLPPipe(stdout, "stdout", false)
	}()
	go func() {
		defer wg.Done()
		stderrOutput, _ = readYTDLPPipe(stderr, "stderr", true)
	}()

	wg.Wait()
	waitErr := cmd.Wait()
	if waitErr == nil {
		return nil
	}

	if stderrOutput != "" {
		utils.ErrorWithFormat("[YoutubeMusic] yt-dlp 错误输出：%s", stderrOutput)
	}
	return newYTDLPFailure("yt-dlp 下载失败", waitErr, stderrOutput)
}

func readYTDLPPipe(r io.ReadCloser, prefix string, capture bool) (string, error) {
	defer r.Close()
	reader := bufio.NewReaderSize(r, 64*1024)
	lines := make([]string, 0, ytDLPMaxCapturedLines)

	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimSpace(line)
			if line != "" {
				utils.DebugWithFormat("[%s] %s", prefix, line)
				if capture {
					lines = append(lines, utils.TruncateString(line, ytDLPMaxCapturedLine))
					if len(lines) > ytDLPMaxCapturedLines {
						lines = lines[1:]
					}
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return strings.Join(lines, "\n"), nil
			}
			return strings.Join(lines, "\n"), err
		}
	}
}
