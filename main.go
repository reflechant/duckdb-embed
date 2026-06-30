package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math/rand"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/duckdb/duckdb-go/v2"
	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf/v2"
	"github.com/xuri/excelize/v2"
)

//go:embed static/*
var staticFiles embed.FS

func main() {
	port := flag.Int("port", 8080, "Port to serve the application on")
	flag.Parse()

	// Initialize in-memory DuckDB
	db, err := sql.Open("duckdb", "")
	if err != nil {
		log.Fatalf("Failed to open DuckDB: %v", err)
	}
	defer db.Close()

	log.Println("Initializing database schema...")
	setupDatabase(db)

	log.Println("Generating mock data...")
	generateData(db, 5000)

	// Setup HTTP routes
	http.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		handleAPIData(w, r, db)
	})
	http.HandleFunc("/api/export", func(w http.ResponseWriter, r *http.Request) {
		handleAPIExport(w, r, db)
	})

	// Serve embedded static files
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatalf("Failed to get sub FS for static files: %v", err)
	}
	http.Handle("/", http.FileServer(http.FS(staticFS)))

	serverAddr := fmt.Sprintf(":%d", *port)
	url := fmt.Sprintf("http://localhost:%d", *port)

	log.Printf("Starting server on %s", url)

	// Launch browser asynchronously
	go func() {
		time.Sleep(500 * time.Millisecond) // Give the server a moment to start
		openBrowser(url)
	}()

	if err := http.ListenAndServe(serverAddr, nil); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

func setupDatabase(db *sql.DB) {
	query := `
	CREATE TABLE metrics (
		id INTEGER PRIMARY KEY,
		req_id VARCHAR,
		status VARCHAR,
		created_at TIMESTAMP,
		duration_ms DOUBLE,
		is_active BOOLEAN,
		category VARCHAR,
		metadata JSON
	)
	`
	if _, err := db.Exec(query); err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}
}

func generateData(db *sql.DB, rows int) {
	statuses := []string{"SUCCESS", "ERROR", "PENDING", "TIMEOUT"}
	categories := []string{"API", "Database", "Cache", "Worker", "Auth"}
	
	conn, err := db.Conn(context.Background())
	if err != nil {
		log.Fatalf("Failed to get DB connection: %v", err)
	}
	defer conn.Close()

	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	now := time.Now()

	err = conn.Raw(func(driverConn interface{}) error {
		appender, err := duckdb.NewAppenderFromConn(driverConn.(driver.Conn), "", "metrics")
		if err != nil {
			return fmt.Errorf("failed to create appender: %w", err)
		}
		defer appender.Close()

		for i := 1; i <= rows; i++ {
			reqID := uuid.New().String()
			status := statuses[rnd.Intn(len(statuses))]
			createdAt := now.Add(-time.Duration(rnd.Intn(10000)) * time.Minute)
			durationMs := rnd.Float64() * 1500.0
			if status == "TIMEOUT" {
				durationMs = 5000.0 + rnd.Float64()*1000.0
			}
			isActive := rnd.Intn(10) > 2 // 70% active
			category := categories[rnd.Intn(len(categories))]
			
			metadataJSON := fmt.Sprintf(`{"region": "us-east-1", "retries": %d, "version": "v1.%d"}`, rnd.Intn(5), rnd.Intn(10))

			err = appender.AppendRow(i, reqID, status, createdAt, durationMs, isActive, category, metadataJSON)
			if err != nil {
				return fmt.Errorf("failed to append row %d: %w", i, err)
			}
		}

		if err := appender.Flush(); err != nil {
			return fmt.Errorf("failed to flush appender: %w", err)
		}
		return nil
	})

	if err != nil {
		log.Fatalf("Failed to generate data: %v", err)
	}
}

// Helper function to build WHERE clause for global search
func buildSearchQuery(searchVal string) (string, []interface{}) {
	if searchVal == "" {
		return "", nil
	}
	searchPattern := "%" + searchVal + "%"
	whereClause := `
		CAST(id AS VARCHAR) LIKE ? OR
		req_id LIKE ? OR
		status LIKE ? OR
		CAST(created_at AS VARCHAR) LIKE ? OR
		CAST(duration_ms AS VARCHAR) LIKE ? OR
		category LIKE ? OR
		CAST(metadata AS VARCHAR) LIKE ?
	`
	whereArgs := make([]interface{}, 7)
	for i := range whereArgs {
		whereArgs[i] = searchPattern
	}
	return whereClause, whereArgs
}

// Helper function to parse sorting columns
func buildOrderQuery(r *http.Request) string {
	var orderClauses []string
	for i := 0; ; i++ {
		colIdxStr := r.FormValue(fmt.Sprintf("order[%d][column]", i))
		if colIdxStr == "" {
			break
		}
		colIdx, err := strconv.Atoi(colIdxStr)
		if err != nil {
			break
		}
		dir := r.FormValue(fmt.Sprintf("order[%d][dir]", i))
		dir = strings.ToUpper(dir)
		if dir != "ASC" && dir != "DESC" {
			dir = "ASC"
		}

		colName := ""
		switch colIdx {
		case 0:
			colName = "id"
		case 1:
			colName = "req_id"
		case 2:
			colName = "status"
		case 3:
			colName = "created_at"
		case 4:
			colName = "duration_ms"
		case 5:
			colName = "is_active"
		case 6:
			colName = "category"
		case 7:
			colName = "metadata"
		}

		if colName != "" {
			orderClauses = append(orderClauses, fmt.Sprintf("%s %s", colName, dir))
		}
	}

	if len(orderClauses) > 0 {
		return strings.Join(orderClauses, ", ")
	}
	return "created_at DESC" // default sort
}

func handleAPIData(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, fmt.Sprintf("Form parse error: %v", err), http.StatusBadRequest)
		return
	}

	drawStr := r.FormValue("draw")
	startStr := r.FormValue("start")
	lengthStr := r.FormValue("length")
	searchVal := r.FormValue("search[value]")

	draw, _ := strconv.Atoi(drawStr)
	start, _ := strconv.Atoi(startStr)
	length, err := strconv.Atoi(lengthStr)
	if err != nil {
		length = 50
	}
	if length == 0 {
		length = 50
	}

	// 1. Get total records count
	var recordsTotal int
	err = db.QueryRow("SELECT COUNT(*) FROM metrics").Scan(&recordsTotal)
	if err != nil {
		sendJSONError(w, draw, fmt.Sprintf("Failed to get total records: %v", err))
		return
	}

	// 2. Build filter conditions (Global search across all columns)
	whereClause, whereArgs := buildSearchQuery(searchVal)

	// 3. Get filtered records count
	recordsFiltered := recordsTotal
	if whereClause != "" {
		err = db.QueryRow("SELECT COUNT(*) FROM metrics WHERE "+whereClause, whereArgs...).Scan(&recordsFiltered)
		if err != nil {
			sendJSONError(w, draw, fmt.Sprintf("Failed to get filtered count: %v", err))
			return
		}
	}

	// 4. Parse sorting (multi-column sorting)
	orderBy := buildOrderQuery(r)

	// 5. Query actual page data
	query := `
		SELECT id, req_id, status, created_at, duration_ms, is_active, category, CAST(metadata AS VARCHAR)
		FROM metrics
	`
	if whereClause != "" {
		query += " WHERE " + whereClause
	}
	query += " ORDER BY " + orderBy

	var rows *sql.Rows
	var qErr error
	if length > 0 {
		query += " LIMIT ? OFFSET ?"
		args := append(whereArgs, length, start)
		rows, qErr = db.Query(query, args...)
	} else {
		rows, qErr = db.Query(query, whereArgs...)
	}
	if qErr != nil {
		sendJSONError(w, draw, fmt.Sprintf("Query execution error: %v", qErr))
		return
	}
	defer rows.Close()

	type Metric struct {
		ID         int     `json:"id"`
		ReqID      string  `json:"req_id"`
		Status     string  `json:"status"`
		CreatedAt  string  `json:"created_at"`
		DurationMs float64 `json:"duration_ms"`
		IsActive   bool    `json:"is_active"`
		Category   string  `json:"category"`
		Metadata   string  `json:"metadata"`
	}

	var results []Metric
	for rows.Next() {
		var m Metric
		var t time.Time
		if err := rows.Scan(&m.ID, &m.ReqID, &m.Status, &t, &m.DurationMs, &m.IsActive, &m.Category, &m.Metadata); err != nil {
			sendJSONError(w, draw, fmt.Sprintf("Row scan error: %v", err))
			return
		}
		m.CreatedAt = t.Format(time.RFC3339)
		results = append(results, m)
	}

	// DataTables SSP JSON format
	response := map[string]interface{}{
		"draw":            draw,
		"recordsTotal":    recordsTotal,
		"recordsFiltered": recordsFiltered,
		"data":            results,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func handleAPIExport(w http.ResponseWriter, r *http.Request, db *sql.DB) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, fmt.Sprintf("Form parse error: %v", err), http.StatusBadRequest)
		return
	}

	searchVal := r.FormValue("search[value]")
	format := r.FormValue("format")
	if format == "" {
		format = "csv"
	}
	log.Printf("API Export Request: format=%q, search=%q, order_col=%q, order_dir=%q", format, searchVal, r.FormValue("order[0][column]"), r.FormValue("order[0][dir]"))

	// 1. Build filter conditions
	whereClause, whereArgs := buildSearchQuery(searchVal)

	// 2. Parse sorting
	orderBy := buildOrderQuery(r)

	// 3. Query all filtered & sorted records (no LIMIT/OFFSET for export)
	query := `
		SELECT id, req_id, status, created_at, duration_ms, is_active, category, CAST(metadata AS VARCHAR)
		FROM metrics
	`
	if whereClause != "" {
		query += " WHERE " + whereClause
	}
	query += " ORDER BY " + orderBy

	rows, err := db.Query(query, whereArgs...)
	if err != nil {
		http.Error(w, fmt.Sprintf("Query error: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	switch format {
	case "csv":
		exportCSV(w, rows)
	case "excel":
		exportExcel(w, rows)
	case "markdown":
		exportMarkdown(w, rows)
	case "pdf":
		exportPDF(w, rows)
	default:
		http.Error(w, "Unsupported export format", http.StatusBadRequest)
	}
}

func exportCSV(w http.ResponseWriter, rows *sql.Rows) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=metrics_export.csv")

	writer := csv.NewWriter(w)
	defer writer.Flush()

	header := []string{"ID", "Request ID", "Status", "Created At", "Duration (ms)", "Active", "Category", "Metadata"}
	if err := writer.Write(header); err != nil {
		log.Printf("Export CSV header write error: %v", err)
		return
	}

	for rows.Next() {
		var id int
		var reqID, status, category, metadata string
		var t time.Time
		var durationMs float64
		var isActive bool

		if err := rows.Scan(&id, &reqID, &status, &t, &durationMs, &isActive, &category, &metadata); err != nil {
			log.Printf("Export row scan error: %v", err)
			return
		}

		activeStr := "false"
		if isActive {
			activeStr = "true"
		}

		row := []string{
			strconv.Itoa(id),
			reqID,
			status,
			t.Format(time.RFC3339),
			fmt.Sprintf("%.2f", durationMs),
			activeStr,
			category,
			metadata,
		}

		if err := writer.Write(row); err != nil {
			log.Printf("Export CSV row write error: %v", err)
			return
		}
	}
}

func exportExcel(w http.ResponseWriter, rows *sql.Rows) {
	f := excelize.NewFile()
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("Error closing Excel file: %v", err)
		}
	}()

	sheetName := "Sheet1"
	headers := []string{"ID", "Request ID", "Status", "Created At", "Duration (ms)", "Active", "Category", "Metadata"}

	// Set headers
	for i, h := range headers {
		colName, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			log.Printf("Excel column conversion error: %v", err)
			return
		}
		f.SetCellValue(sheetName, colName+"1", h)
	}

	// Stylize Header
	style, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Color: "000000"},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"E0E0E0"}, Pattern: 1},
	})
	if err == nil {
		f.SetRowStyle(sheetName, 1, 1, style)
	}

	// Column Widths
	widths := map[string]float64{"A": 10, "B": 40, "C": 15, "D": 25, "E": 15, "F": 10, "G": 15, "H": 50}
	for col, width := range widths {
		f.SetColWidth(sheetName, col, col, width)
	}

	rowIdx := 2
	for rows.Next() {
		var id int
		var reqID, status, category, metadata string
		var t time.Time
		var durationMs float64
		var isActive bool

		if err := rows.Scan(&id, &reqID, &status, &t, &durationMs, &isActive, &category, &metadata); err != nil {
			log.Printf("Excel export row scan error: %v", err)
			return
		}

		activeStr := "false"
		if isActive {
			activeStr = "true"
		}

		f.SetCellValue(sheetName, fmt.Sprintf("A%d", rowIdx), id)
		f.SetCellValue(sheetName, fmt.Sprintf("B%d", rowIdx), reqID)
		f.SetCellValue(sheetName, fmt.Sprintf("C%d", rowIdx), status)
		f.SetCellValue(sheetName, fmt.Sprintf("D%d", rowIdx), t.Format(time.RFC3339))
		f.SetCellValue(sheetName, fmt.Sprintf("E%d", rowIdx), durationMs)
		f.SetCellValue(sheetName, fmt.Sprintf("F%d", rowIdx), activeStr)
		f.SetCellValue(sheetName, fmt.Sprintf("G%d", rowIdx), category)
		f.SetCellValue(sheetName, fmt.Sprintf("H%d", rowIdx), metadata)

		rowIdx++
	}

	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", "attachment; filename=metrics_export.xlsx")

	if err := f.Write(w); err != nil {
		log.Printf("Export Excel write error: %v", err)
	}
}

func exportMarkdown(w http.ResponseWriter, rows *sql.Rows) {
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=metrics_export.md")

	headers := []string{"ID", "Request ID", "Status", "Created At", "Duration (ms)", "Active", "Category", "Metadata"}

	var sb strings.Builder
	sb.WriteString("| ")
	sb.WriteString(strings.Join(headers, " | "))
	sb.WriteString(" |\n")

	sb.WriteString("|")
	for range headers {
		sb.WriteString("---|")
	}
	sb.WriteString("\n")

	if _, err := w.Write([]byte(sb.String())); err != nil {
		log.Printf("Markdown header write error: %v", err)
		return
	}
	sb.Reset()

	for rows.Next() {
		var id int
		var reqID, status, category, metadata string
		var t time.Time
		var durationMs float64
		var isActive bool

		if err := rows.Scan(&id, &reqID, &status, &t, &durationMs, &isActive, &category, &metadata); err != nil {
			log.Printf("Markdown export row scan error: %v", err)
			return
		}

		activeStr := "false"
		if isActive {
			activeStr = "true"
		}

		esc := func(s string) string {
			return strings.ReplaceAll(s, "|", "\\|")
		}

		sb.WriteString("| ")
		sb.WriteString(strconv.Itoa(id))
		sb.WriteString(" | ")
		sb.WriteString(esc(reqID))
		sb.WriteString(" | ")
		sb.WriteString(esc(status))
		sb.WriteString(" | ")
		sb.WriteString(esc(t.Format(time.RFC3339)))
		sb.WriteString(" | ")
		sb.WriteString(fmt.Sprintf("%.2f", durationMs))
		sb.WriteString(" | ")
		sb.WriteString(activeStr)
		sb.WriteString(" | ")
		sb.WriteString(esc(category))
		sb.WriteString(" | ")
		sb.WriteString(esc(metadata))
		sb.WriteString(" |\n")

		if _, err := w.Write([]byte(sb.String())); err != nil {
			log.Printf("Markdown row write error: %v", err)
			return
		}
		sb.Reset()
	}
}

func exportPDF(w http.ResponseWriter, rows *sql.Rows) {
	pdf := gofpdf.New("L", "mm", "A4", "")
	pdf.SetMargins(10, 10, 10)
	pdf.SetAutoPageBreak(true, 10)

	pdf.SetHeaderFunc(func() {
		pdf.SetFont("Arial", "B", 12)
		pdf.Cell(0, 10, "Metrics Explorer Export")
		pdf.Ln(10)

		pdf.SetFont("Arial", "B", 8)
		pdf.SetFillColor(220, 220, 220)
		headers := []string{"ID", "Request ID", "Status", "Created At", "Duration (ms)", "Active", "Category", "Metadata"}
		widths := []float64{12, 62, 20, 38, 22, 12, 20, 91}
		for i, h := range headers {
			lnVal := 0
			if i == len(headers)-1 {
				lnVal = 1
			}
			pdf.CellFormat(widths[i], 7, h, "1", lnVal, "L", true, 0, "")
		}
	})

	pdf.SetFooterFunc(func() {
		pdf.SetY(-15)
		pdf.SetFont("Arial", "I", 8)
		pdf.CellFormat(0, 10, fmt.Sprintf("Page %d", pdf.PageNo()), "", 0, "C", false, 0, "")
	})

	pdf.AddPage()
	pdf.SetFont("Arial", "", 8)

	widths := []float64{12, 62, 20, 38, 22, 12, 20, 91}
	fill := false
	pdf.SetFillColor(245, 245, 245)

	for rows.Next() {
		var id int
		var reqID, status, category, metadata string
		var t time.Time
		var durationMs float64
		var isActive bool

		if err := rows.Scan(&id, &reqID, &status, &t, &durationMs, &isActive, &category, &metadata); err != nil {
			log.Printf("PDF export row scan error: %v", err)
			return
		}

		activeStr := "false"
		if isActive {
			activeStr = "true"
		}

		trunc := func(s string, maxLen int) string {
			if len(s) > maxLen {
				return s[:maxLen-3] + "..."
			}
			return s
		}

		pdf.CellFormat(widths[0], 6, strconv.Itoa(id), "1", 0, "R", fill, 0, "")
		pdf.CellFormat(widths[1], 6, trunc(reqID, 36), "1", 0, "L", fill, 0, "")
		pdf.CellFormat(widths[2], 6, trunc(status, 12), "1", 0, "L", fill, 0, "")
		pdf.CellFormat(widths[3], 6, t.Format(time.RFC3339), "1", 0, "L", fill, 0, "")
		pdf.CellFormat(widths[4], 6, fmt.Sprintf("%.2f", durationMs), "1", 0, "R", fill, 0, "")
		pdf.CellFormat(widths[5], 6, activeStr, "1", 0, "C", fill, 0, "")
		pdf.CellFormat(widths[6], 6, trunc(category, 12), "1", 0, "L", fill, 0, "")
		pdf.CellFormat(widths[7], 6, trunc(metadata, 60), "1", 1, "L", fill, 0, "")

		fill = !fill
	}

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", "attachment; filename=metrics_export.pdf")
	if err := pdf.Output(w); err != nil {
		log.Printf("Export PDF write error: %v", err)
	}
}

func sendJSONError(w http.ResponseWriter, draw int, errMsg string) {
	log.Printf("API Error: %s", errMsg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"draw":            draw,
		"recordsTotal":    0,
		"recordsFiltered": 0,
		"data":            []interface{}{},
		"error":           errMsg,
	})
}

func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	if err != nil {
		log.Printf("Could not open browser automatically: %v", err)
	}
}
