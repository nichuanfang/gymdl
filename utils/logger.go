package utils

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/nichuanfang/gymdl/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var (
	loggerInstance        *zap.Logger
	sugaredLoggerInstance *zap.SugaredLogger
)

// ======================= 彩色与对齐配置 =======================

// 彩色等级输出（控制台）
var colorLevelEncoder = func(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	switch l {
	case zapcore.DebugLevel:
		enc.AppendString("\033[1;36mDEBUG\033[0m")
	case zapcore.InfoLevel:
		enc.AppendString("\033[1;32mINFO \033[0m")
	case zapcore.WarnLevel:
		enc.AppendString("\033[1;33mWARN \033[0m")
	case zapcore.ErrorLevel:
		enc.AppendString("\033[1;31mERROR\033[0m")
	case zapcore.FatalLevel:
		enc.AppendString("\033[1;35mFATAL\033[0m")
	default:
		enc.AppendString(fmt.Sprintf("%-5s", l.CapitalString()))
	}
}

// 调整 caller 显示宽度
func paddedCallerEncoder(caller zapcore.EntryCaller, enc zapcore.PrimitiveArrayEncoder) {
	if caller.Defined {
		enc.AppendString(fmt.Sprintf("%-25s", caller.TrimmedPath()))
	} else {
		enc.AppendString(fmt.Sprintf("%-25s", ""))
	}
}

// ======================= Encoder 构造 =======================

// 控制台输出编码器（彩色）
func newConsoleEncoder() zapcore.Encoder {
	return zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		TimeKey:      "T",
		LevelKey:     "L",
		CallerKey:    "C",
		MessageKey:   "M",
		EncodeTime:   zapcore.TimeEncoderOfLayout("15:04:05"),
		EncodeLevel:  colorLevelEncoder,
		EncodeCaller: paddedCallerEncoder,
	})
}

// 文件输出编码器（无颜色）
func newFileEncoder() zapcore.Encoder {
	return zapcore.NewConsoleEncoder(zapcore.EncoderConfig{
		TimeKey:        "T",
		LevelKey:       "L",
		CallerKey:      "C",
		MessageKey:     "M",
		EncodeTime:     zapcore.TimeEncoderOfLayout("2006-01-02 15:04:05"),
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeCaller:   paddedCallerEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
	})
}

// ======================= 初始化 =======================

// InitLogger 初始化 Logger
func InitLogger(cfg *config.LogConfig) error {
	if cfg == nil {
		return fmt.Errorf("日志配置为空")
	}

	var level zapcore.Level
	switch cfg.Level {
	case 1:
		level = zap.DebugLevel
	case 2:
		level = zap.InfoLevel
	case 3:
		level = zap.WarnLevel
	case 4:
		level = zap.ErrorLevel
	default:
		level = zap.InfoLevel
	}

	var cores []zapcore.Core

	if cfg.Mode == 1 || cfg.Mode == 3 { // 控制台
		consoleCore := zapcore.NewCore(newConsoleEncoder(), zapcore.Lock(os.Stdout), level)
		cores = append(cores, consoleCore)
	}

	if (cfg.Mode == 2 || cfg.Mode == 3) && cfg.File != "" { // 文件输出
		if err := os.MkdirAll(filepath.Dir(cfg.File), 0755); err != nil {
			return fmt.Errorf("创建日志目录失败: %v", err)
		}
		f, err := os.OpenFile(cfg.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("打开日志文件失败: %v", err)
		}
		fileCore := zapcore.NewCore(newFileEncoder(), zapcore.AddSync(f), level)
		cores = append(cores, fileCore)
	}

	core := zapcore.NewTee(cores...)
	loggerInstance = zap.New(core, zap.AddCaller(), zap.AddCallerSkip(1), zap.Hooks(broadcastHook))
	sugaredLoggerInstance = loggerInstance.Sugar()

	return nil
}

// 同步日志（程序退出前调用）
func Sync() {
	if loggerInstance != nil {
		_ = loggerInstance.Sync()
	}
}

// 获取 logger 实例
func Logger() *zap.Logger {
	return loggerInstance
}

func SugaredLogger() *zap.SugaredLogger {
	return sugaredLoggerInstance
}

// ======================= 简化封装 =======================

// 通用输出：带图标格式化信息
func logWithIcon(level zapcore.Level, icon string, msg string) {
	if sugaredLoggerInstance == nil {
		fmt.Println(icon, msg) // fallback 输出
		return
	}

	switch level {
	case zapcore.DebugLevel:
		sugaredLoggerInstance.Debugf("%s %s", icon, msg)
	case zapcore.InfoLevel:
		sugaredLoggerInstance.Infof("%s %s", icon, msg)
	case zapcore.WarnLevel:
		sugaredLoggerInstance.Warnf("%s %s", icon, msg)
	case zapcore.ErrorLevel:
		sugaredLoggerInstance.Errorf("%s %s", icon, msg)
	default:
		sugaredLoggerInstance.Infof("%s %s", icon, msg)
	}
}

// ======================= 快捷调用函数 =======================

// 成功类
func Success(args ...interface{}) { logWithIcon(zapcore.InfoLevel, "✅", fmt.Sprint(args...)) }
func Successf(format string, args ...interface{}) {
	logWithIcon(zapcore.InfoLevel, "✅", fmt.Sprintf(format, args...))
}

// 服务状态类
func ServiceIsOn(args ...interface{}) { logWithIcon(zapcore.InfoLevel, "⚙️", fmt.Sprint(args...)) }
func ServiceIsOnf(format string, args ...interface{}) {
	logWithIcon(zapcore.InfoLevel, "⚙️", fmt.Sprintf(format, args...))
}

// 网络状态类
func NetworkHealth(args ...interface{}) { logWithIcon(zapcore.InfoLevel, "🌐", fmt.Sprint(args...)) }
func NetworkHealthf(format string, args ...interface{}) {
	logWithIcon(zapcore.InfoLevel, "🌐", fmt.Sprintf(format, args...))
}

// 停止类
func Stop(args ...interface{}) { logWithIcon(zapcore.InfoLevel, "🛑", fmt.Sprint(args...)) }
func Stopf(format string, args ...interface{}) {
	logWithIcon(zapcore.InfoLevel, "🛑", fmt.Sprintf(format, args...))
}

// 通用信息类
func Info(args ...interface{}) { logWithIcon(zapcore.InfoLevel, "💡", fmt.Sprint(args...)) }
func Infof(format string, args ...interface{}) {
	logWithIcon(zapcore.InfoLevel, "💡", fmt.Sprintf(format, args...))
}

// 警告类
func Warning(args ...interface{}) { logWithIcon(zapcore.WarnLevel, "⚠️", fmt.Sprint(args...)) }
func Warningf(format string, args ...interface{}) {
	logWithIcon(zapcore.WarnLevel, "⚠️", fmt.Sprintf(format, args...))
}

// 调试类
func Debug(args ...interface{}) { logWithIcon(zapcore.DebugLevel, "🐞", fmt.Sprint(args...)) }
func Debugf(format string, args ...interface{}) {
	logWithIcon(zapcore.DebugLevel, "🐞", fmt.Sprintf(format, args...))
}

// 严重错误类
func Critical(args ...interface{}) { logWithIcon(zapcore.ErrorLevel, "🔥", fmt.Sprint(args...)) }
func Criticalf(format string, args ...interface{}) {
	logWithIcon(zapcore.ErrorLevel, "🔥", fmt.Sprintf(format, args...))
}

// 格式化类(不带图标)
func InfoWithFormat(format string, args ...interface{}) {
	Logger().Info(fmt.Sprintf(format, args...))
}
func WarnWithFormat(format string, args ...interface{}) {
	Logger().Warn(fmt.Sprintf(format, args...))
}
func ErrorWithFormat(format string, args ...interface{}) {
	Logger().Error(fmt.Sprintf(format, args...))
}
func DebugWithFormat(format string, args ...interface{}) {
	Logger().Debug(fmt.Sprintf(format, args...))
}
func FatalWithFormat(format string, args ...interface{}) {
	Logger().Fatal(fmt.Sprintf(format, args...))
}
