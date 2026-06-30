package main

import (
	"log"
	"time"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}
	loc, err := time.LoadLocation(cfg.Timezone)
	if err != nil {
		log.Fatalf("时区加载失败 %s: %v", cfg.Timezone, err)
	}
	bot := NewBot(cfg, loc)
	if err := bot.Run(); err != nil {
		log.Fatal(err)
	}
}
