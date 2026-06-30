# Embedded DuckDB & DataTables Explorer in Go

This is an example project demonstrating how to embed **DuckDB** in a Go application and build rich, high-performance datatables utilizing a **"no-build"** frontend architecture. The entire application compiles into a single, self-contained **fat binary**.

## Key Features

- **Embedded Database:** Utilizes the Go bindings for [DuckDB](https://duckdb.org/) (`github.com/duckdb/duckdb-go/v2`) in-memory, initializing schema and seeding 5,000 mock request logs automatically on startup.
- **"No-Build" Frontend:** Zero dependencies on Node.js, Webpack, Vite, or npm. Plain HTML, CSS, and Vanilla JS are served directly using Go's native `embed` package.
- **Server-Side Processing (SSP):** Handles filtering, paging, and multi-column sorting entirely in Go via DuckDB SQL queries, enabling rapid pagination over large datasets.
- **Dense Datadog-Inspired UI:** Modern, compact dashboard styling with customized status badges, monospace fields for UUIDs/dates, and optimal layout density.
- **Rich Data Exports:** Supports server-side exporting of the currently filtered dataset into multiple formats:
  - **CSV** (via `encoding/csv`)
  - **Excel** (via `github.com/xuri/excelize/v2`)
  - **Markdown** tables
  - **PDF** (via `github.com/jung-kurt/gofpdf/v2`)
- **Auto-Launch:** Automatically opens the application in your default web browser upon server initialization.

---

## Project Structure

```text
├── main.go            # Go entrypoint: DB setup, HTTP routes, SSP query builders, and export logic
├── go.mod             # Go module definition and dependencies
├── go.sum             # Go module checksums
└── static/            # Static assets served via go:embed
    ├── index.html     # HTML page structure with table definition
    ├── css/
    │   └── style.css  # Custom dense UI theme (Datadog inspired)
    ├── js/
    │   └── app.js     # DataTables configuration, AJAX requests, and export triggers
    └── vendor/        # Pre-downloaded jQuery, DataTables, and PDF/Excel export helpers
```

---

## How it Works

### 1. DataTables Server-Side Processing (SSP)
The front-end DataTables instance in [`static/js/app.js`](static/js/app.js) is configured with `serverSide: true`. When a user paginates, searches, or sorts, DataTables sends a request with query parameters (e.g. `start`, `length`, `search[value]`, `order[0][column]`) to `/api/data`.

The Go backend in [`main.go`](main.go):
1. Computes total and filtered record counts.
2. Dynamically builds a SQL `WHERE` clause for global search.
3. Parses multi-column ordering parameters to build `ORDER BY`.
4. Executes the query using `LIMIT` and `OFFSET` in DuckDB.
5. Returns a structured JSON payload conforming to the DataTables SSP specification.

### 2. Streamlined Exports
Export buttons trigger requests to `/api/export` with the current search query and sort parameters intact. The backend queries the complete matching dataset (without pagination limits) and formats it on the fly, streaming the file directly back to the user's browser.

---

## Getting Started

### Prerequisites

You need [Go](https://go.dev/) (version 1.26 or higher is recommended) installed on your system.

### Running the Application

To run the project in development mode:

```bash
go run main.go
```

The application will spin up a web server at `http://localhost:8080` and attempt to launch your browser automatically.

### Compiling a Fat Binary

To compile the entire server and frontend into a single executable binary:

```bash
go build -o duckdb-embed-go main.go
```

Run the generated binary:

```bash
./duckdb-embed-go
```
