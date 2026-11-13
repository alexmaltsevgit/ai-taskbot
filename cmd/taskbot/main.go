package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/alexmaltsevgit/ai-taskbot/internal/taskbot"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
func run() error {
	syscall.Umask(0077)
	mode := "bot"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	token := os.Getenv("WORKER_TOKEN")
	if len(token) < 32 {
		return fmt.Errorf("WORKER_TOKEN must contain at least 32 characters")
	}
	if mode == "worker" {
		server := &http.Server{Addr: ":8090", Handler: taskbot.WorkerHandler(token, taskbot.InferWithCodex), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 210 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
		go func() {
			<-ctx.Done()
			shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			_ = server.Shutdown(shutdown)
		}()
		err := server.ListenAndServe()
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
	if mode != "bot" {
		return fmt.Errorf("usage: taskbot [bot|worker]")
	}
	raw, err := os.ReadFile(env("CONFIG_FILE", "config.json"))
	if err != nil {
		return fmt.Errorf("configuration unreadable")
	}
	var config taskbot.Config
	if err = json.Unmarshal(raw, &config); err != nil {
		return fmt.Errorf("invalid configuration JSON")
	}
	if err = taskbot.ValidateConfig(config); err != nil {
		return err
	}
	statePath := env("STATE_FILE", "data/state.json")
	lock, err := taskbot.LockState(statePath)
	if err != nil {
		return err
	}
	defer lock.Close()
	store, err := taskbot.OpenStore(statePath)
	if err != nil {
		return err
	}
	githubToken, err := taskbot.GitHubTokenProvider()
	if err != nil {
		return err
	}
	telegramToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if telegramToken == "" {
		return fmt.Errorf("TELEGRAM_BOT_TOKEN required")
	}
	service := &taskbot.Service{Store: store, Config: config, Github: &taskbot.GithubClient{BaseURL: "https://api.github.com", HTTP: taskbot.NewHTTPClient(45 * time.Second), Token: githubToken}, Model: &taskbot.ModelClient{Endpoint: env("WORKER_URL", "http://worker:8090"), Token: token, HTTP: taskbot.NewHTTPClient(200 * time.Second)}}
	telegram := &taskbot.TelegramClient{BaseURL: "https://api.telegram.org/bot" + telegramToken + "/", HTTP: taskbot.NewHTTPClient(50 * time.Second)}
	return taskbot.RunBot(ctx, service, telegram)
}
func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}
