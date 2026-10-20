package logger

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"

	"workflow-api/internal/config"
)

type Logger struct {
	*zap.Logger
	fileSink *lumberjack.Logger
}

func New(cfg config.LoggingConfig) (*Logger, error) {
	level := zapcore.InfoLevel
	if err := level.UnmarshalText([]byte(strings.ToLower(cfg.Level))); err != nil {
		return nil, err
	}

	encoderConfig := zap.NewProductionEncoderConfig()
	encoderConfig.TimeKey = "timestamp"
	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	encoder := zapcore.NewJSONEncoder(encoderConfig)
	cores := make([]zapcore.Core, 0, 2)

	var fileSink *lumberjack.Logger
	if strings.TrimSpace(cfg.FilePath) != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.FilePath), 0o755); err != nil {
			return nil, err
		}

		fileSink = &lumberjack.Logger{
			Filename:   cfg.FilePath,
			MaxSize:    cfg.MaxSizeMB,
			MaxBackups: cfg.MaxBackups,
			MaxAge:     cfg.MaxAgeDays,
			Compress:   cfg.Compress,
		}
		cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(fileSink), level))
	}

	if cfg.Console {
		cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(os.Stdout), level))
	}

	if len(cores) == 0 {
		return nil, errors.New("logger has no configured output")
	}

	return &Logger{
		Logger:   zap.New(zapcore.NewTee(cores...), zap.AddCaller()),
		fileSink: fileSink,
	}, nil
}

func (l *Logger) Close() error {
	if l == nil {
		return nil
	}

	syncErr := l.Sync()
	if l.fileSink == nil {
		return syncErr
	}

	return errors.Join(syncErr, l.fileSink.Close())
}
