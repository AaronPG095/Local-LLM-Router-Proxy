package proxy

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"time"
)

// forwardSSE streams an upstream SSE body line-by-line to w with zero full-body buffering.
// It uses bufio.Reader.ReadBytes('\n') so payloads >64KB never fail.
// Returns the timestamp of the first byte written and any read/write error.
func forwardSSE(w http.ResponseWriter, body io.Reader, onDataLine func(payload []byte)) (firstByte time.Time, err error) {
	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReaderSize(body, 32*1024)

	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if firstByte.IsZero() {
				firstByte = time.Now()
			}
			if _, writeErr := w.Write(line); writeErr != nil {
				return firstByte, writeErr
			}
			if flusher != nil {
				flusher.Flush()
			}

			trimmed := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmed, []byte("data:")) && onDataLine != nil {
				payload := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
				if len(payload) > 0 && !bytes.Equal(payload, []byte("[DONE]")) {
					onDataLine(payload)
				}
			}
		}

		if readErr != nil {
			if readErr == io.EOF {
				return firstByte, nil
			}
			return firstByte, readErr
		}
	}
}
