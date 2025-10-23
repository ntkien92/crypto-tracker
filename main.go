package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // SQLite driver (no CGO)
)

var (
	dbFile = "data.db"
)

type Config struct {
	TelegramToken  string `json:"telegram_token"`
	TelegramChatID string `json:"telegram_chat_id"`
	SlackWebhook   string `json:"slack_webhook"`
}

func loadConfig() Config {
	data, err := os.ReadFile("config.json")
	if err != nil {
		log.Fatalf("Không đọc được config.json: %v", err)
	}
	var cfg Config
	json.Unmarshal(data, &cfg)
	return cfg
}

var coins = []string{"bitcoin", "ethereum", "binancecoin"}

type PriceResponse map[string]map[string]float64

// === DATABASE INIT ===
func initDB() (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbFile)
	if err != nil {
		return nil, err
	}
	createTable := `
	CREATE TABLE IF NOT EXISTS prices (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		coin TEXT NOT NULL,
		price_usd REAL NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);
	`
	if _, err := db.Exec(createTable); err != nil {
		return nil, err
	}
	return db, nil
}

// === FETCH PRICES ===
func fetchPrices() (map[string]float64, error) {
	url := fmt.Sprintf("https://api.coingecko.com/api/v3/simple/price?ids=%s&vs_currencies=usd",
		strings.Join(coins, ","),
	)
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("coingecko returned %d", resp.StatusCode)
	}

	var data PriceResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	out := map[string]float64{}
	for _, c := range coins {
		if v, ok := data[c]["usd"]; ok {
			out[c] = v
		} else {
			return nil, errors.New("missing usd for " + c)
		}
	}
	return out, nil
}

// === STORE TO DB ===
func savePrices(db *sql.DB, prices map[string]float64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare("INSERT INTO prices (coin, price_usd) VALUES (?, ?)")
	if err != nil {
		return err
	}
	defer stmt.Close()

	for coin, price := range prices {
		if _, err := stmt.Exec(coin, price); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// === TELEGRAM ===
func sendTelegramMessage(cfg Config, text string) error {
	if cfg.TelegramToken == "" || cfg.TelegramChatID == "" {
		return errors.New("TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID not set")
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", cfg.TelegramToken)
	body := fmt.Sprintf(`{"chat_id":"%s","text":%q}`, cfg.TelegramChatID, text)
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("telegram returned %d", resp.StatusCode)
	}
	return nil
}

// === SLACK ===
func sendSlackMessage(cfg Config, text string) error {
	if cfg.SlackWebhook == "" {
		return nil
	}
	payload := map[string]string{"text": text}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(cfg.SlackWebhook, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("slack returned %d", resp.StatusCode)
	}
	return nil
}

// === HELPER ===
func formatMessage(prices map[string]float64, lastPrices map[string]float64) string {
	now := time.Now().Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf("📊 *Crypto Prices (USD)*\nTime: %s\n", now)
	for _, c := range coins {
		symbol := map[string]string{
			"bitcoin":     "BTC",
			"ethereum":    "ETH",
			"binancecoin": "BNB",
		}[c]
		msg += fmt.Sprintf("\n%s: $%.2f", symbol, prices[c])
		if lastPrices[c] > 0 {
			msg += fmt.Sprintf(" Change: %.2f$", prices[c]-lastPrices[c])
		}
	}
	return msg
}

// === MAIN ===
func main() {
	log.Println("Starting crypto tracker...")
	cfg := loadConfig()

	db, err := initDB()
	if err != nil {
		log.Fatalf("DB init failed: %v", err)
	}
	defer db.Close()

	var lastPrices map[string]float64
	runJob := func() {
		prices, err := fetchPrices()
		if err != nil {
			log.Printf("fetch error: %v", err)
			return
		}

		if err := savePrices(db, prices); err != nil {
			log.Printf("save error: %v", err)
			return
		}

		msg := formatMessage(prices, lastPrices)
		lastPrices = prices
		if err := sendTelegramMessage(cfg, msg); err != nil {
			log.Printf("telegram error: %v", err)
		}

		if err := sendSlackMessage(cfg, msg); err != nil {
			log.Printf("slack error: %v", err)
		}
		log.Println("✅ Prices pushed successfully!")
	}

	runJob()

	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		runJob()
	}
}
