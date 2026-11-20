package config

import "testing"

func TestLogDefaultsToInfoFileRotationAt500MB(t *testing.T) {
	c := &Config{}
	c.setDefaults()
	if c.Log == nil || c.Log.Mode != 1 || c.Log.Level != 2 || c.Log.File != "data/logs/run.log" || c.Log.MaxSizeMB != 500 {
		t.Fatalf("log defaults = %+v", c.Log)
	}
}

func TestLogMaxSizeCanBeOverridden(t *testing.T) {
	c := &Config{Log: &LogConfig{Mode: 2, Level: 2, File: "data/logs/run.log", MaxSizeMB: 256}}
	c.setDefaults()
	if c.Log.MaxSizeMB != 256 {
		t.Fatalf("max size = %d", c.Log.MaxSizeMB)
	}
}
