package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestGetLogsDisabledReturnsAppLoggingError(t *testing.T) {
	handler := NewHandler(&config.Config{LoggingToFile: false}, "", nil)

	rec := performLogsRequest(t, handler.GetLogs, http.MethodGet, "/v0/management/logs")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error != "logging to file disabled" || !strings.Contains(body.Message, "application log file output is disabled") {
		t.Fatalf("body = %#v", body)
	}
}

func TestGetLogsLoadsLinesWithLimitAndAfter(t *testing.T) {
	dir := t.TempDir()
	first := time.Date(2026, 5, 30, 10, 0, 0, 0, time.Local)
	second := first.Add(time.Minute)
	content := strings.Join([]string{
		formatTestLogLine(first, "first app log line"),
		formatTestLogLine(second, "second app log line"),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, defaultLogFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	handler := NewHandler(&config.Config{LoggingToFile: true}, "", nil)
	handler.SetLogDirectory(dir)

	target := fmt.Sprintf("/v0/management/logs?limit=1&after=%d", first.Unix())
	rec := performLogsRequest(t, handler.GetLogs, http.MethodGet, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Lines           []string `json:"lines"`
		LineCount       int      `json:"line-count"`
		LatestTimestamp int64    `json:"latest-timestamp"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Lines) != 1 || !strings.Contains(body.Lines[0], "second app log line") {
		t.Fatalf("lines = %#v", body.Lines)
	}
	if body.LineCount != 2 {
		t.Fatalf("line-count = %d", body.LineCount)
	}
	if body.LatestTimestamp != second.Unix() {
		t.Fatalf("latest-timestamp = %d want %d", body.LatestTimestamp, second.Unix())
	}
}

func formatTestLogLine(ts time.Time, message string) string {
	return fmt.Sprintf("[%s] [--------] [info ] %s", ts.Format("2006-01-02 15:04:05"), message)
}

func performLogsRequest(t *testing.T, fn gin.HandlerFunc, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(method, target, nil)
	fn(ginCtx)
	return rec
}
