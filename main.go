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

type ColumnDef struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

var tableColumns []ColumnDef

func main() {
	port := flag.Int("port", 8080, "Port to serve the application on")
	dataFile := flag.String("data", "", "Path to JSON file to load into DuckDB")
	flag.Parse()

	// Initialize in-memory DuckDB
	db, err := sql.Open("duckdb", "")
	if err != nil {
		log.Fatalf("Failed to open DuckDB: %v", err)
	}
	defer db.Close()

	log.Println("Initializing database schema...")
	setupDatabase(db, *dataFile)

	// Fetch dynamic schema
	tableColumns = fetchSchema(db)
	if len(tableColumns) == 0 {
		log.Fatalf("No columns found in the metrics table. Database initialization might have failed.")
	}

	// Setup HTTP routes
	http.HandleFunc("/api/schema", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(tableColumns)
	})
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

func setupDatabase(db *sql.DB, dataFile string) {
	if dataFile != "" {
		log.Printf("Loading data from JSON file: %s", dataFile)
		// Try using read_json_auto
		query := fmt.Sprintf("CREATE TABLE metrics AS SELECT * FROM read_json_auto('%s')", dataFile)
		if _, err := db.Exec(query); err != nil {
			log.Fatalf("Failed to create table from JSON: %v", err)
		}
	} else {
		log.Println("No JSON file provided. Generating mock data...")
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
		generateData(db, 5000)
	}
}

func fetchSchema(db *sql.DB) []ColumnDef {
	rows, err := db.Query("PRAGMA table_info('metrics')")
	if err != nil {
		log.Fatalf("Failed to get table schema: %v", err)
	}
	defer rows.Close()

	var columns []ColumnDef
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notnull bool
		var dflt_value interface{}
		var pk bool
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt_value, &pk); err != nil {
			log.Fatalf("Failed to scan PRAGMA table_info: %v", err)
		}
		columns = append(columns, ColumnDef{Name: name, Type: typ})
	}
	return columns
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

// Helper function to build WHERE clause for global search dynamically
func buildSearchQuery(searchVal string) (string, []interface{}) {
	if searchVal == "" {
		return "", nil
	}
	searchPattern := "%" + searchVal + "%"
	var clauses []string
	var args []interface{}

	for _, col := range tableColumns {
		// Use TRY_CAST or CAST to VARCHAR for all columns
		clauses = append(clauses, fmt.Sprintf("CAST(%s AS VARCHAR) LIKE ?", col.Name))
		args = append(args, searchPattern)
	}

	whereClause := strings.Join(clauses, " OR ")
	return whereClause, args
}

// Helper function to parse sorting columns dynamically
func buildOrderQuery(r *http.Request) string {
	var orderClauses []string
	for i := 0; ; i++ {
		colIdxStr := r.FormValue(fmt.Sprintf("order[%d][column]", i))
		if colIdxStr == "" {
			break
		}
		colIdx, err := strconv.Atoi(colIdxStr)
		if err != nil || colIdx < 0 || colIdx >= len(tableColumns) {
			break
		}
		dir := r.FormValue(fmt.Sprintf("order[%d][dir]", i))
		dir = strings.ToUpper(dir)
		if dir != "ASC" && dir != "DESC" {
			dir = "ASC"
		}

		colName := tableColumns[colIdx].Name
		orderClauses = append(orderClauses, fmt.Sprintf("%s %s", colName, dir))
	}

	if len(orderClauses) > 0 {
		return strings.Join(orderClauses, ", ")
	}
	if len(tableColumns) > 0 {
		return tableColumns[0].Name + " DESC" // default sort by first column if no order
	}
	return ""
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

	// 5. Query actual page data dynamically
	query := "SELECT * FROM metrics"
	if whereClause != "" {
		query += " WHERE " + whereClause
	}
	if orderBy != "" {
		query += " ORDER BY " + orderBy
	}

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

	cols, err := rows.Columns()
	if err != nil {
		sendJSONError(w, draw, fmt.Sprintf("Failed to get columns: %v", err))
		return
	}

	var results []map[string]interface{}
	for rows.Next() {
		columnPointers := make([]interface{}, len(cols))
		columnValues := make([]interface{}, len(cols))
		for i := range columnValues {
			columnPointers[i] = &columnValues[i]
		}

		if err := rows.Scan(columnPointers...); err != nil {
			sendJSONError(w, draw, fmt.Sprintf("Row scan error: %v", err))
			return
		}

		rowMap := make(map[string]interface{})
		for i, colName := range cols {
			val := columnValues[i]
			if b, ok := val.([]byte); ok {
				rowMap[colName] = string(b)
			} else if t, ok := val.(time.Time); ok {
				rowMap[colName] = t.Format(time.RFC3339)
			} else {
				rowMap[colName] = val
			}
		}
		results = append(results, rowMap)
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
	
	// 1. Build filter conditions
	whereClause, whereArgs := buildSearchQuery(searchVal)

	// 2. Parse sorting
	orderBy := buildOrderQuery(r)

	// 3. Query all filtered & sorted records (no LIMIT/OFFSET for export)
	query := "SELECT * FROM metrics"
	if whereClause != "" {
		query += " WHERE " + whereClause
	}
	if orderBy != "" {
		query += " ORDER BY " + orderBy
	}

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

func fetchRowValuesAsStrings(rows *sql.Rows, cols []string) ([]string, error) {
	columnPointers := make([]interface{}, len(cols))
	columnValues := make([]interface{}, len(cols))
	for i := range columnValues {
		columnPointers[i] = &columnValues[i]
	}

	if err := rows.Scan(columnPointers...); err != nil {
		return nil, err
	}

	rowStr := make([]string, len(cols))
	for i := range columnValues {
		val := columnValues[i]
		if val == nil {
			rowStr[i] = ""
		} else {
			switch v := val.(type) {
			case []byte:
				rowStr[i] = string(v)
			case time.Time:
				rowStr[i] = v.Format(time.RFC3339)
			case bool:
				if v {
					rowStr[i] = "true"
				} else {
					rowStr[i] = "false"
				}
			case float64:
				// Avoid super long floats
				strVal := fmt.Sprintf("%f", v)
				rowStr[i] = strings.TrimRight(strings.TrimRight(strVal, "0"), ".")
			default:
				rowStr[i] = fmt.Sprintf("%v", v)
			}
		}
	}
	return rowStr, nil
}

func exportCSV(w http.ResponseWriter, rows *sql.Rows) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=metrics_export.csv")

	writer := csv.NewWriter(w)
	defer writer.Flush()

	header := make([]string, len(tableColumns))
	for i, col := range tableColumns {
		header[i] = col.Name
	}

	if err := writer.Write(header); err != nil {
		log.Printf("Export CSV header write error: %v", err)
		return
	}

	cols, _ := rows.Columns()
	for rows.Next() {
		rowStr, err := fetchRowValuesAsStrings(rows, cols)
		if err != nil {
			log.Printf("Export row scan error: %v", err)
			return
		}

		if err := writer.Write(rowStr); err != nil {
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
	
	// Set headers
	for i, col := range tableColumns {
		colName, err := excelize.ColumnNumberToName(i + 1)
		if err != nil {
			log.Printf("Excel column conversion error: %v", err)
			return
		}
		f.SetCellValue(sheetName, colName+"1", col.Name)
		// Set a default col width
		f.SetColWidth(sheetName, colName, colName, 20)
	}

	// Stylize Header
	style, err := f.NewStyle(&excelize.Style{
		Font: &excelize.Font{Bold: true, Color: "000000"},
		Fill: excelize.Fill{Type: "pattern", Color: []string{"E0E0E0"}, Pattern: 1},
	})
	if err == nil {
		f.SetRowStyle(sheetName, 1, 1, style)
	}

	rowIdx := 2
	cols, _ := rows.Columns()
	for rows.Next() {
		rowStr, err := fetchRowValuesAsStrings(rows, cols)
		if err != nil {
			log.Printf("Excel export row scan error: %v", err)
			return
		}

		for i, val := range rowStr {
			colName, _ := excelize.ColumnNumberToName(i + 1)
			f.SetCellValue(sheetName, fmt.Sprintf("%s%d", colName, rowIdx), val)
		}
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

	headers := make([]string, len(tableColumns))
	for i, col := range tableColumns {
		headers[i] = col.Name
	}

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

	cols, _ := rows.Columns()
	for rows.Next() {
		rowStr, err := fetchRowValuesAsStrings(rows, cols)
		if err != nil {
			log.Printf("Markdown export row scan error: %v", err)
			return
		}

		esc := func(s string) string {
			return strings.ReplaceAll(s, "|", "\\|")
		}

		sb.WriteString("| ")
		for i, val := range rowStr {
			sb.WriteString(esc(val))
			if i < len(rowStr)-1 {
				sb.WriteString(" | ")
			}
		}
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

	totalWidth := 277.0 // A4 landscape width (297) - margins (20)
	colCount := len(tableColumns)
	if colCount == 0 {
		colCount = 1
	}
	baseWidth := totalWidth / float64(colCount)

	pdf.SetHeaderFunc(func() {
		pdf.SetFont("Arial", "B", 12)
		pdf.Cell(0, 10, "Metrics Explorer Export")
		pdf.Ln(10)

		pdf.SetFont("Arial", "B", 8)
		pdf.SetFillColor(220, 220, 220)
		
		for i, col := range tableColumns {
			lnVal := 0
			if i == len(tableColumns)-1 {
				lnVal = 1
			}
			pdf.CellFormat(baseWidth, 7, col.Name, "1", lnVal, "L", true, 0, "")
		}
	})

	pdf.SetFooterFunc(func() {
		pdf.SetY(-15)
		pdf.SetFont("Arial", "I", 8)
		pdf.CellFormat(0, 10, fmt.Sprintf("Page %d", pdf.PageNo()), "", 0, "C", false, 0, "")
	})

	pdf.AddPage()
	pdf.SetFont("Arial", "", 8)

	fill := false
	pdf.SetFillColor(245, 245, 245)

	cols, _ := rows.Columns()
	for rows.Next() {
		rowStr, err := fetchRowValuesAsStrings(rows, cols)
		if err != nil {
			log.Printf("PDF export row scan error: %v", err)
			return
		}

		// rough truncation based on width (approx 2mm per char at 8pt, very rough)
		maxChars := int(baseWidth / 1.5)
		if maxChars < 5 {
			maxChars = 5
		}

		trunc := func(s string, maxLen int) string {
			if len(s) > maxLen {
				return s[:maxLen-3] + "..."
			}
			return s
		}

		for i, val := range rowStr {
			lnVal := 0
			if i == len(rowStr)-1 {
				lnVal = 1
			}
			pdf.CellFormat(baseWidth, 6, trunc(val, maxChars), "1", lnVal, "L", fill, 0, "")
		}

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
