package handler

import (
	"bufio"
	"encoding/json"
	"errors"
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

// readSSELines calls fn for each line of an SSE stream. It avoids bufio.Scanner,
// which aborts the whole stream on the >1MB events Copilot legitimately sends.
func readSSELines(body io.Reader, fn func(line string) error) error {
	reader := bufio.NewReaderSize(body, 64*1024)
	var builder strings.Builder
	for {
		chunk, err := reader.ReadString('\n')
		if len(chunk) > 0 {
			builder.WriteString(chunk)
			if strings.HasSuffix(chunk, "\n") {
				line := strings.TrimRight(builder.String(), "\r\n")
				builder.Reset()
				if fnErr := fn(line); fnErr != nil {
					return fnErr
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				if builder.Len() > 0 {
					return fn(strings.TrimRight(builder.String(), "\r\n"))
				}
				return nil
			}
			return err
		}
	}
}

func readSSE(body io.Reader, handler func(eventType, data string) error) error {
	var eventType string
	done := false
	err := readSSELines(body, func(line string) error {
		switch {
		case strings.HasPrefix(line, "event: "):
			eventType = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				done = true
				return errStreamDone
			}
			if err := handler(eventType, data); err != nil {
				return err
			}
			eventType = ""
		}
		return nil
	})
	if done && err == errStreamDone {
		return nil
	}
	return err
}

// errStreamDone unwinds readSSELines when the [DONE] sentinel arrives.
var errStreamDone = errors.New("sse: stream done")
