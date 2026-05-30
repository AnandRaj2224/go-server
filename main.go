package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	_ "github.com/lib/pq"
)

// --- CONFIGURATION ---
// In a real app, load these from a .env file!
const (
	dbConnStr    = "host=localhost port=5432 user=journal_admin password=supersecret dbname=journal_db sslmode=disable"
	colabBaseURL = "https://YOUR-NGROK-URL.ngrok-free.app" // Update this whenever you restart Colab
)

var db *sql.DB

// --- DATA BLUEPRINTS (Structs) ---
type JournalEntry struct {
	ID      int    `json:"id,omitempty"`
	Content string `json:"content"`
	Date    string `json:"date"`
}

type DateRangeRequest struct {
	StartDate string         `json:"start_date"`
	EndDate   string         `json:"end_date"`
	Entries   []JournalEntry `json:"entries"`
}

// --- DATABASE SETUP ---
func initDB() {
	var err error
	db, err = sql.Open("postgres", dbConnStr)
	if err != nil {
		log.Fatalf("Failed to open DB connection: %v", err)
	}

	if err = db.Ping(); err != nil {
		log.Fatalf("Failed to ping DB: %v", err)
	}
	fmt.Println("📦 Successfully connected to PostgreSQL!")
}

func createTables() {
	query := `
	CREATE TABLE IF NOT EXISTS entries (
		id SERIAL PRIMARY KEY,
		content TEXT NOT NULL,
		entry_date DATE NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	if _, err := db.Exec(query); err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}
	fmt.Println("✅ Database tables verified and ready!")
}

// --- COLAB HELPER FUNCTION ---
// askColab securely forwards JSON to your Python model and waits for the RAG insight
func askColab(endpoint string, payload any) ([]byte, error) {
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	// 10-second timeout so the Go gateway doesn't freeze if Colab is slow
	client := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequest("POST", colabBaseURL+endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("colab unreachable: %v", err)
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}

// --- ROUTE HANDLERS ---

// 1. CREATE & ANALYZE (Daily Door)
func createEntryHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST allowed", http.StatusMethodNotAllowed)
		return
	}

	var entry JournalEntry
	if err := json.NewDecoder(r.Body).Decode(&entry); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Save to Postgres
	query := `INSERT INTO entries (content, entry_date) VALUES ($1, $2) RETURNING id`
	err := db.QueryRow(query, entry.Content, entry.Date).Scan(&entry.ID)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	// Forward to Colab for Daily Analysis
	insight, err := askColab("/api/predict/day", entry)
	if err != nil {
		fmt.Printf("Warning: Colab analysis failed: %v\n", err)
		// We still return 200 because the entry was successfully saved to the DB
		fmt.Fprintf(w, `{"message": "Saved to DB, but analysis failed", "id": %d}`, entry.ID)
		return
	}

	// Send successful response to frontend
	w.Header().Set("Content-Type", "application/json")
	w.Write(insight)
}

// 2. READ (Get all entries for the frontend UI)
func getEntriesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Only GET allowed", http.StatusMethodNotAllowed)
		return
	}

	rows, err := db.Query(`SELECT id, content, TO_CHAR(entry_date, 'YYYY-MM-DD') FROM entries ORDER BY entry_date DESC`)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var entries []JournalEntry
	for rows.Next() {
		var e JournalEntry
		if err := rows.Scan(&e.ID, &e.Content, &e.Date); err != nil {
			continue
		}
		entries = append(entries, e)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

// 3. RANGE ANALYSIS (Batch processing)
func analyzeRangeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Only POST allowed", http.StatusMethodNotAllowed)
		return
	}

	var batch DateRangeRequest
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	// Forward the entire batch to Colab
	insight, err := askColab("/api/predict/range", batch)
	if err != nil {
		http.Error(w, "Colab analysis failed", http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(insight)
}

// --- MAIN SERVER START ---
func main() {
	initDB()
	createTables()
	defer db.Close()

	mux := http.NewServeMux()

	// Registering the routes
	mux.HandleFunc("/api/entries", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			createEntryHandler(w, r)
		case http.MethodGet:
			getEntriesHandler(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/api/analyze/range", analyzeRangeHandler)

	fmt.Println("🚀 Gateway running concurrently on port 8080...")
	log.Fatal(http.ListenAndServe(":8080", mux))
}
