package monitor

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/nichuanfang/gymdl/config"
	"github.com/nichuanfang/gymdl/core"
	"github.com/nichuanfang/gymdl/processor"
	"github.com/nichuanfang/gymdl/utils"
)

var umTempDir = filepath.Join("data", "temp", "um")

// HandleEvent 处理文件新增
func HandleEvent(path string, cfg *config.Config) error {
	utils.InfoWithFormat("[Um] 开始处理文件: %s", filepath.Base(path))
	//临时输出目录
	tempDir := processor.BuildOutputDir(umTempDir)
	defer os.RemoveAll(tempDir)
	//判断文件后缀 如果是非解密文件 跳过
	if utils.Contains(EncryptedExts(), filepath.Ext(path)) {
		//调用um工具解密
		cmd := BuildUmCmd(path, tempDir)
		output, err := cmd.CombinedOutput()
		utils.InfoWithFormat(string(output))
		if err != nil {
			utils.ErrorWithFormat("[Um] 音乐解密失败: %v", err)
			return err
		}
		err = os.Remove(path)
		if err != nil {
			utils.ErrorWithFormat("[Um] 源文件删除失败: %v", err)
			return err
		}
		//整理
		err = tidy(findTrack(tempDir), cfg)
		_ = processor.RemoveTempDir(tempDir)
		if err != nil {
			utils.ErrorWithFormat(err.Error())
			return err
		}
	} else {
		//直接整理
		err := tidy(path, cfg)
		if err != nil {
			utils.ErrorWithFormat(err.Error())
			return err
		}
	}
	utils.InfoWithFormat("[Um] 文件: %s 处理成功", filepath.Base(path))
	return nil
}

// BuildUmCmd 构建一个执行 um 命令的 *exec.Cmd
func BuildUmCmd(inputFile, outputDir string) *exec.Cmd {
	// um 命令参数列表
	args := []string{
		"-i", inputFile, // 输入文件
		"-o", outputDir, // 输出目录
		"--overwrite", // 覆盖输出文件
	}
	return exec.Command("um", args...)
}

// EncryptedExts 加密后缀
func EncryptedExts() []string {
	return []string{".ncm", ".qmc3", ".qmcflac", ".mflac", ".mgg",
		".mflac0", ".mgg1", ".mggl", ".mgalaxy", ".mflach", ".xm",
		".kwm", ".mflac", ".kgm", ".vpr", ".kgg", ".x2m", ".x3m",
		".xm", ".mg3d", ".qta"}
}

// findTrack 识别目录下的文件
func findTrack(dir string) string {
	var track string

	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			utils.ErrorWithFormat("[Um] 访问文件失败: %v", err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		track = path
		return fs.SkipDir
	})

	if track == "" {
		utils.ErrorWithFormat("[Um] 未找到解密后的文件")
	}
	return track
}

// tidy 整理
func tidy(path string, cfg *config.Config) error {
	if path == "" {
		return errors.New("文件路径为空")
	}
	utils.InfoWithFormat("[Um] 开始整理文件: %s", path)
	var err error
	switch cfg.Tidy.Mode {
	case 1:
		err = tidyToLocal(path, cfg)
	case 2:
		err = tidyToWebDAV(path, core.GlobalWebDAV, cfg)
	default:
		utils.WarnWithFormat("[Um] 未知的整理模式: %s", path)
		return errors.New("未知的整理模式")
	}
	if err != nil {
		return err
	}
	return nil
}
