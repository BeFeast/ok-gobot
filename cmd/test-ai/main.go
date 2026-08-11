package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"ok-gobot/internal/ai"
	"ok-gobot/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	provider := strings.ToLower(strings.TrimSpace(cfg.AI.Provider))
	if cfg.AI.APIKey == "" && provider != "chatgpt" && provider != "openai-codex" && provider != "droid" {
		fmt.Println("No API key configured")
		os.Exit(1)
	}

	client, err := ai.NewClient(ai.ProviderConfig{
		Name:               cfg.AI.Provider,
		APIKey:             cfg.AI.APIKey,
		BaseURL:            cfg.AI.BaseURL,
		Model:              cfg.AI.Model,
		ChatGPTAuthFile:    cfg.AI.ChatGPT.AuthFile,
		ChatGPTCodexHome:   cfg.AI.ChatGPT.CodexHome,
		ChatGPTCodexBinary: cfg.AI.ChatGPT.BinaryPath,
	})
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	messages := []ai.Message{
		{Role: "system", Content: "You are a helpful assistant. Reply briefly."},
		{Role: "user", Content: "Say 'Hello from Kimi K2.5!' and one fun fact about lobsters"},
	}

	fmt.Printf("Testing %s...\n", cfg.AI.Model)
	fmt.Println("Sending request...")

	response, err := client.Complete(ctx, messages)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	fmt.Printf("\n✅ Response:\n%s\n", response)
}
