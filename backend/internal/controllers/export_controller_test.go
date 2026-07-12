package controllers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUpdateSettings_RejectsBadBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c := &ExportController{} // handlers that fail before touching deps on bad input
	r := gin.New()
	r.PUT("/settings", c.UpdateSettings)

	req := httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{"enabled":"notabool"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if _, ok := body["error"]; !ok {
		t.Fatal("expected error field")
	}
}
