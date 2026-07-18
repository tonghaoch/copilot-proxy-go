package handler

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func writeSSE(w http.ResponseWriter, flusher http.Flusher, eventType string, data any) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventType, string(jsonData)); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeSSEError(w http.ResponseWriter, flusher http.Flusher, message string) {
	writeSSE(w, flusher, "error", StreamErrorEvent{
		Type:  "error",
		Error: StreamErrBody{Type: "api_error", Message: message},
	})
}

func readSSE(body io.Reader, handler func(eventType, data string) error) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)
	var eventType string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				return nil
			}
			if err := handler(eventType, data); err != nil {
				return err
			}
			eventType = ""
		}
	}
	return scanner.Err()
}
