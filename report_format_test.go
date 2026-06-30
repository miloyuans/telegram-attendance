package main

import "testing"

func TestReportMultiFormatDefaultDisabled(t *testing.T) {
	cfg := defaultConfig()
	if cfg.ReportMultiFormatEnabled {
		t.Fatal("report_multi_format_enabled should be false by default")
	}
}

func TestReportMultiFormatEnvOverride(t *testing.T) {
	t.Setenv("CONFIG_FILE", t.TempDir()+"/missing-config.json")
	t.Setenv("BOT_TOKEN", "123456:dummy-token")
	t.Setenv("REPORT_MULTI_FORMAT_ENABLED", "true")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if !cfg.ReportMultiFormatEnabled {
		t.Fatal("REPORT_MULTI_FORMAT_ENABLED=true should enable multi-format reports")
	}
}
