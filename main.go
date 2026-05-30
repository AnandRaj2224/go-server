package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	_ "github.com/lib/pq"
)

// --- CONFIGURATION ---
const (
	dbConnStr = "host=localhost port=5432 user=journal_admin password=supersecret dbname=journal_db sslmode=disable"
	// Update this when your Colab Ngrok restarts
	fastAPIURL = "https://safeguard-tableful-krypton.ngrok-free.dev" 
)

var db *sql.DB

// --- STRUCTS ---
type Entry struct {
	ID           int       `json:"id"`
	UserID       string    `json:"user_id"`
	Content      string    `json:"content"`
	MoodScore    int       `json:"mood_score"`
	MoodLabel    string    `json:"mood_label"`
	GoalAnalysis string    `json:"goal_analysis"`
	CreatedAt    time.Time `json:"created_at"`
}

type Goal struct {
	ID        int       `json:"id"`
	UserID    string    `json:"user_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

// FastAPI Request/Response mapping
type FastApiAnalyzeReq struct {
	Text string `json:"text"`
}
type FastApiAnalyzeRes struct {
	MoodType        string `json:"mood_type"`
	DominantEmotion string `json:"dominant_emotion"`
	Summary         string `json:"summary"`
}

// --- DATABASE INIT ---
func initDB() {
	var err error
	db, err = sql.Open("postgres", dbConnStr)
	if err != nil {
		log.Fatalf("Failed to open DB: %v", err)
	}
	if err = db.Ping(); err != nil {
		log.Fatalf("Failed to ping DB: %v", err)
	}
	fmt.Println("📦 Connected to PostgreSQL!")
}

// --- MIDDLEWARE ---
// Enables your Vite/React frontend to talk to this backend
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- HANDLERS ---

func checkHealth(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, `{"status":"ok"}`)
}

// GET /entries/{userId}
func getEntries(w http.ResponseWriter, r *http.Request) {
	userId := r.PathValue("userId")
	rows, err := db.Query("SELECT id, user_id, content, mood_score, mood_label, goal_analysis, created_at FROM entries WHERE user_id = $1 ORDER BY created_at DESC", userId)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.UserID, &e.Content, &e.MoodScore, &e.MoodLabel, &e.GoalAnalysis, &e.CreatedAt); err == nil {
			entries = append(entries, e)
		}
	}
	
	// Ensure we return an empty array [] instead of null if no entries exist
	if entries == nil {
		entries = []Entry{}
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

// POST /entries/
func createEntry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID  string `json:"user_id"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	_, err := db.Exec("INSERT INTO entries (user_id, content) VALUES ($1, $2)", req.UserID, req.Content)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	
	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, `{"message":"Entry created"}`)
}

// DELETE /entries/{id}
func deleteEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	db.Exec("DELETE FROM entries WHERE id = $1", id)
	w.WriteHeader(http.StatusOK)
}

// POST /analyze/batch/
// This acts as a proxy: fetches entries, asks FastAPI to analyze them, updates DB
func analyzeBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntryIDs []int `json:"entry_ids"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	for _, id := range req.EntryIDs {
		// 1. Get content from DB
		var content string
		err := db.QueryRow("SELECT content FROM entries WHERE id = $1", id).Scan(&content)
		if err != nil {
			continue
		}

		// 2. Forward to Python FastAPI Ngrok URL
		apiReq := FastApiAnalyzeReq{Text: content}
		jsonData, _ := json.Marshal(apiReq)
		resp, err := http.Post(fastAPIURL+"/analyze", "application/json", bytes.NewBuffer(jsonData))
		
		if err != nil || resp.StatusCode != 200 {
			log.Printf("FastAPI connection failed for entry %d: %v", id, err)
			continue
		}

		// 3. Parse Python response
		var aiRes FastApiAnalyzeRes
		json.NewDecoder(resp.Body).Decode(&aiRes)
		resp.Body.Close()

		// 4. Map the mood type to a 1-10 score for the Recharts graph in React
		score := 5 // Default Neutral
		if aiRes.MoodType == "positive" {
			score = 8
		} else if aiRes.MoodType == "negative" {
			score = 3
		}

		// 5. Update Database
		db.Exec(`UPDATE entries SET mood_score = $1, mood_label = $2, goal_analysis = $3 WHERE id = $4`,
			score, aiRes.DominantEmotion, aiRes.Summary, id)
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{"message":"Batch analyzed successfully"}`)
}

// --- GOALS CRUD ---
func getGoals(w http.ResponseWriter, r *http.Request) {
	userId := r.PathValue("userId")
	rows, _ := db.Query("SELECT id, user_id, title, created_at FROM goals WHERE user_id = $1 ORDER BY created_at DESC", userId)
	defer rows.Close()

	var goals []Goal
	for rows.Next() {
		var g Goal
		if err := rows.Scan(&g.ID, &g.UserID, &g.Title, &g.CreatedAt); err == nil {
			goals = append(goals, g)
		}
	}
	if goals == nil {
		goals = []Goal{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(goals)
}

func createGoal(w http.ResponseWriter, r *http.Request) {
	var g Goal
	json.NewDecoder(r.Body).Decode(&g)
	db.Exec("INSERT INTO goals (user_id, title) VALUES ($1, $2)", g.UserID, g.Title)
	w.WriteHeader(http.StatusCreated)
}

func deleteGoal(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	db.Exec("DELETE FROM goals WHERE id = $1", id)
	w.WriteHeader(http.StatusOK)
}

// --- CHAT ENDPOINT ---
func chatAssistant(w http.ResponseWriter, r *http.Request) {
	// Since the FastAPI only handles analysis currently, this acts as a placeholder
	// You can easily wire this up to an LLM API (like OpenAI/Gemini) later
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"reply": "I have reviewed your recent entries. It seems like academic stress is a recurring trigger for you this week. Try to take short breaks during your study sessions."}`)
}

func main() {
	initDB()
	defer db.Close()

	mux := http.NewServeMux()

	// Register Routes
	mux.HandleFunc("GET /", checkHealth)
	
	mux.HandleFunc("GET /entries/{userId}", getEntries)
	mux.HandleFunc("POST /entries/", createEntry)
	mux.HandleFunc("DELETE /entries/{id}", deleteEntry)
	
	mux.HandleFunc("POST /analyze/batch/", analyzeBatch)
	
	mux.HandleFunc("GET /goals/{userId}", getGoals)
	mux.HandleFunc("POST /goals/", createGoal)
	mux.HandleFunc("DELETE /goals/{id}", deleteGoal)
	
	mux.HandleFunc("POST /chat/", chatAssistant)

	// Wrap the mux with the CORS middleware
	handler := corsMiddleware(mux)

	fmt.Println("🚀 Go API Gateway running on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", handler))
}