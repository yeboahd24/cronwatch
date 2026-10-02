package config

import (
	"os"
	"path/filepath"
)

type Config struct {
	DataDir string
}

func Load() (Config, error) {
	if v := os.Getenv("CRONWATCH_DATA_DIR"); v != "" {
		return Config{DataDir: v}, nil
	}

	dir, err := os.UserConfigDir()
	if err != nil {
		return Config{}, err
	}

	return Config{
		DataDir: filepath.Join(dir, "cronwatch"),
	}, nil
}
