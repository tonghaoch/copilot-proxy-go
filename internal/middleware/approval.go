package middleware

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

// ManualApproval returns a middleware that prompts the operator via CLI
// to approve or reject each incoming request.
func ManualApproval(next http.Handler) http.Handler {
	reader := bufio.NewReader(os.Stdin)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always allow health check
		if r.URL.Path == "/" {
			next.ServeHTTP(w, r)
			return
		}

		fmt.Printf("\n  Incoming request: %s %s\n", r.Method, r.URL.Path)
		fmt.Print("  Accept? [y/N]: ")

		input, err := reader.ReadString('\n')
		if err != nil {
			slog.Error("failed to read approval input", "error", err)
			reject(w)
			return
		}

		input = strings.TrimSpace(strings.ToLower(input))
		if input != "y" && input != "yes" {
			slog.Info("request rejected by operator", "path", r.URL.Path)
			reject(w)
			return
		}

		slog.Info("request approved by operator", "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func reject(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{
			"message": "Request rejected",
			"type":    "permission_error",
		},
	})
}
