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
	telegramBotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	telegramChatID   = os.Getenv("TELEGRAM_CHAT_ID")
	dbFile           = "data.db"
)

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
func sendTelegramMessage(text string) error {
	if telegramBotToken == "" || telegramChatID == "" {
		return errors.New("TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID not set")
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", telegramBotToken)
	body := fmt.Sprintf(`{"chat_id":"%s","text":%q}`, telegramChatID, text)
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

// === HELPER ===
func formatMessage(prices map[string]float64) string {
	now := time.Now().Format("2006-01-02 15:04:05")
	msg := fmt.Sprintf("📊 *Crypto Prices (USD)*\nTime: %s\n", now)
	for _, c := range coins {
		symbol := map[string]string{
			"bitcoin":     "BTC",
			"ethereum":    "ETH",
			"binancecoin": "BNB",
		}[c]
		msg += fmt.Sprintf("\n%s: $%.2f", symbol, prices[c])
	}
	return msg
}

// === MAIN ===
func main() {
	log.Println("Starting crypto tracker...")

	db, err := initDB()
	if err != nil {
		log.Fatalf("DB init failed: %v", err)
	}
	defer db.Close()

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

		msg := formatMessage(prices)
		if err := sendTelegramMessage(msg); err != nil {
			log.Printf("telegram error: %v", err)
			return
		}
		log.Println("Pushed prices successfully!")
	}

	runJob()

	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		runJob()
	}
}
