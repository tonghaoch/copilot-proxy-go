package handler

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

func (s *responsesStreamWriter) streamCustomInput() error {
	raw := s.args.String()
	if s.inputPos < 0 {
		var found bool
		s.inputPos, found = customInputStart(raw)
		if !found {
			s.inputPos = -1
			return nil
		}
	}
	if s.inputEnd {
		return nil
	}
	text, end, closed, err := decodeCustomInputPart(raw, s.inputPos)
	if err != nil {
		return fmt.Errorf("invalid custom tool input: %w", err)
	}
	s.inputPos, s.inputEnd = end, closed
	if text == "" {
		return nil
	}
	s.customText.WriteString(text)
	return s.send("response.custom_tool_call_input.delta", map[string]any{
		"item_id": s.itemID, "output_index": s.outputIdx, "delta": text,
	})
}

func customInputStart(raw string) (int, bool) {
	dec := json.NewDecoder(strings.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return 0, false
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return 0, false
		}
		if key == "input" {
			i := int(dec.InputOffset())
			for i < len(raw) && isJSONSpace(raw[i]) {
				i++
			}
			if i == len(raw) || raw[i] != ':' {
				return 0, false
			}
			i++
			for i < len(raw) && isJSONSpace(raw[i]) {
				i++
			}
			if i == len(raw) || raw[i] != '"' {
				return 0, false
			}
			return i + 1, true
		}
		var ignored json.RawMessage
		if err := dec.Decode(&ignored); err != nil {
			return 0, false
		}
	}
	return 0, false
}

func isJSONSpace(b byte) bool {
	return b == ' ' || b == '\n' || b == '\r' || b == '\t'
}

// Keep incomplete escapes and surrogate pairs until the next JSON delta.
func decodeCustomInputPart(raw string, start int) (string, int, bool, error) {
	end := start
	for end < len(raw) && raw[end] != '"' {
		if raw[end] != '\\' {
			end++
			continue
		}
		if end+1 >= len(raw) {
			break
		}
		if raw[end+1] != 'u' {
			end += 2
			continue
		}
		if end+6 > len(raw) {
			break
		}
		code, err := strconv.ParseUint(raw[end+2:end+6], 16, 16)
		if err != nil {
			return "", start, false, err
		}
		next := end + 6
		if code >= 0xd800 && code <= 0xdbff {
			if next >= len(raw) || (raw[next] == '\\' && next+1 >= len(raw)) {
				break
			}
			if raw[next] == '\\' && raw[next+1] == 'u' {
				if next+6 > len(raw) {
					break
				}
				low, err := strconv.ParseUint(raw[next+2:next+6], 16, 16)
				if err != nil {
					return "", start, false, err
				}
				if low >= 0xdc00 && low <= 0xdfff {
					end = next + 6
					continue
				}
			}
		}
		end = next
	}
	closed := end < len(raw) && raw[end] == '"'
	if end == start {
		return "", end, closed, nil
	}
	var text string
	if err := json.Unmarshal([]byte(`"`+raw[start:end]+`"`), &text); err != nil {
		return "", start, false, err
	}
	return text, end, closed, nil
}
