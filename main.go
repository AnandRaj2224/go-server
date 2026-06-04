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

const (
	dbConnStr  = "host=localhost port=5432 user=journal_admin password=supersecret dbname=journal_db sslmode=disable"
	fastAPIURL = "https://safeguard-tableful-krypton.ngrok-free.dev"
)

var db *sql.DB

// --- EXISTING STRUCTS ---
type Entry struct {
	ID               int             `json:"id"`
	UserID           string          `json:"user_id"`
	Content          string          `json:"content"`
	MoodScore        int             `json:"mood_score"`
	MoodLabel        string          `json:"mood_label"`
	GoalAnalysis     string          `json:"goal_analysis"`
	Guidance         string          `json:"guidance"`
	DetectedEmotions json.RawMessage `json:"detected_emotions"` 
	Keywords         json.RawMessage `json:"keywords"`
	Triggers         json.RawMessage `json:"triggers"`
	CreatedAt        time.Time       `json:"created_at"`
}

type Goal struct {
	ID        int       `json:"id"`
	UserID    string    `json:"user_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
}

type FastApiAnalyzeReq struct {
	Text string `json:"text"`
}
type EmotionDetail struct {
	Emotion    string  `json:"emotion"`
	Score      float64 `json:"score"`
	Percentage float64 `json:"percentage"`
}
type KeywordDetail struct {
	Keyword string  `json:"keyword"`
	Score   float64 `json:"score"`
}
type FastApiAnalyzeRes struct {
	MoodType         string          `json:"mood_type"`
	DominantEmotion  string          `json:"dominant_emotion"`
	Summary          string          `json:"summary"`
	Guidance         string          `json:"guidance"`
	DetectedEmotions []EmotionDetail `json:"detected_emotions"`
	Keywords         []KeywordDetail `json:"keywords"`
	Triggers         []string        `json:"triggers"`
}

// --- NEW STRUCTS FOR WEEKLY ANALYSIS ---
type WeeklyReqEntry struct {
	Day  string `json:"day"`
	Text string `json:"text"`
}
type FastApiWeeklyReq struct {
	Entries []WeeklyReqEntry `json:"entries"`
}
type DailyAnalysis struct {
	Day             string   `json:"day"`
	DominantEmotion string   `json:"dominant_emotion"`
	MoodType        string   `json:"mood_type"`
	Triggers        []string `json:"triggers"`
	Summary         string   `json:"summary"`
}
type FastApiWeeklyRes struct {
	DailyAnalysis  []DailyAnalysis `json:"daily_analysis"`
	WeeklySummary  string          `json:"weekly_summary"`
	WeeklyGuidance string          `json:"weekly_guidance"`
}

// --- DB INIT & MIDDLEWARE ---
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

func getEntries(w http.ResponseWriter, r *http.Request) {
	userId := r.PathValue("userId")
	query := `SELECT id, user_id, content, mood_score, mood_label, goal_analysis, guidance, 
              detected_emotions, keywords, triggers, created_at 
              FROM entries WHERE user_id = $1 ORDER BY created_at DESC`
	rows, err := db.Query(query, userId)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		var emotionsBytes, keywordsBytes, triggersBytes []byte
		
		err := rows.Scan(&e.ID, &e.UserID, &e.Content, &e.MoodScore, &e.MoodLabel, &e.GoalAnalysis, &e.Guidance, 
			&emotionsBytes, &keywordsBytes, &triggersBytes, &e.CreatedAt)
			
		if err == nil {
			e.DetectedEmotions = emotionsBytes
			e.Keywords = keywordsBytes
			e.Triggers = triggersBytes
			entries = append(entries, e)
		}
	}
	if entries == nil {
		entries = []Entry{}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

func createEntry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID  string `json:"user_id"`
		Content string `json:"content"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	db.Exec("INSERT INTO entries (user_id, content) VALUES ($1, $2)", req.UserID, req.Content)
	w.WriteHeader(http.StatusCreated)
}

func deleteEntry(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	db.Exec("DELETE FROM entries WHERE id = $1", id)
	w.WriteHeader(http.StatusOK)
}

func analyzeBatch(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EntryIDs []int `json:"entry_ids"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	for _, id := range req.EntryIDs {
		var content string
		err := db.QueryRow("SELECT content FROM entries WHERE id = $1", id).Scan(&content)
		if err != nil {
			continue
		}

		apiReq := FastApiAnalyzeReq{Text: content}
		jsonData, _ := json.Marshal(apiReq)
		resp, err := http.Post(fastAPIURL+"/analyze", "application/json", bytes.NewBuffer(jsonData))
		
		if err != nil || resp.StatusCode != 200 {
			continue
		}

		var aiRes FastApiAnalyzeRes
		json.NewDecoder(resp.Body).Decode(&aiRes)
		resp.Body.Close()

		score := 5 
		if aiRes.MoodType == "positive" {
			score = 8
		} else if aiRes.MoodType == "negative" {
			score = 3
		}

		emotionsJSON, _ := json.Marshal(aiRes.DetectedEmotions)
		keywordsJSON, _ := json.Marshal(aiRes.Keywords)
		triggersJSON, _ := json.Marshal(aiRes.Triggers)

		query := `UPDATE entries SET mood_score = $1, mood_label = $2, goal_analysis = $3, guidance = $4, 
                  detected_emotions = $5, keywords = $6, triggers = $7 WHERE id = $8`
		db.Exec(query, score, aiRes.DominantEmotion, aiRes.Summary, aiRes.Guidance, 
                string(emotionsJSON), string(keywordsJSON), string(triggersJSON), id)
	}
	w.WriteHeader(http.StatusOK)
}

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

// --- NEW: WEEKLY ANALYSIS PROXY ---
func generateWeeklyAnalysis(w http.ResponseWriter, r *http.Request) {
	userId := r.PathValue("userId")
	
	// 1. Pull the last 7 entries for this user
	rows, err := db.Query("SELECT content, created_at FROM entries WHERE user_id = $1 ORDER BY created_at DESC LIMIT 7", userId)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var reqPayload FastApiWeeklyReq
	for rows.Next() {
		var content string
		var createdAt time.Time
		if err := rows.Scan(&content, &createdAt); err == nil {
			// Format the timestamp into a weekday string (e.g., "Monday")
			reqPayload.Entries = append(reqPayload.Entries, WeeklyReqEntry{
				Day:  createdAt.Format("Monday"),
				Text: content,
			})
		}
	}

	// Reverse array so chronological order is passed to Python
	for i, j := 0, len(reqPayload.Entries)-1; i < j; i, j = i+1, j-1 {
		reqPayload.Entries[i], reqPayload.Entries[j] = reqPayload.Entries[j], reqPayload.Entries[i]
	}

	if len(reqPayload.Entries) == 0 {
		http.Error(w, `{"error": "Not enough entries to generate a report"}`, http.StatusBadRequest)
		return
	}

	// 2. Fire the batch to FastAPI
	jsonData, _ := json.Marshal(reqPayload)
	resp, err := http.Post(fastAPIURL+"/weekly-analysis", "application/json", bytes.NewBuffer(jsonData))
	
	if err != nil || resp.StatusCode != 200 {
		log.Printf("FastAPI Weekly connection failed: %v", err)
		http.Error(w, `{"error": "Failed to connect to AI Model"}`, http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// 3. Pass the structured response back to React
	var aiRes FastApiWeeklyRes
	json.NewDecoder(resp.Body).Decode(&aiRes)
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(aiRes)
}

func main() {
	initDB()
	defer db.Close()
	mux := http.NewServeMux()
	
	mux.HandleFunc("GET /", checkHealth)
	mux.HandleFunc("GET /entries/{userId}", getEntries)
	mux.HandleFunc("POST /entries/", createEntry)
	mux.HandleFunc("DELETE /entries/{id}", deleteEntry)
	mux.HandleFunc("POST /analyze/batch/", analyzeBatch)
	mux.HandleFunc("GET /goals/{userId}", getGoals)
	mux.HandleFunc("POST /goals/", createGoal)
	mux.HandleFunc("DELETE /goals/{id}", deleteGoal)
	
	// Register the new weekly analysis endpoint
	mux.HandleFunc("GET /weekly-analysis/{userId}", generateWeeklyAnalysis)

	handler := corsMiddleware(mux)
	fmt.Println("🚀 Go API Gateway running on http://localhost:8080")
	log.Fatal(http.ListenAndServe(":8080", handler))
}