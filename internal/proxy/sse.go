package proxy

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"time"
)

const defaultMaxCaptureBytes = 4 * 1024 * 1024 // 4 MiB

// forwardSSE streams an upstream SSE body line-by-line to w with zero full-body buffering.
// It uses bufio.Reader.ReadBytes('\n') so payloads >64KB never fail.
func forwardSSE(w http.ResponseWriter, body io.Reader, onDataLine func(payload []byte)) (firstByte time.Time, err error) {
	firstByte, _, err = forwardSSEWithCapture(w, body, 0, onDataLine)
	return firstByte, err
}

// forwardSSEWithCapture streams an upstream SSE body line-by-line to w while optionally teeing
// raw SSE bytes into a bounded capture buffer for L1/L2 cache replay.
func forwardSSEWithCapture(w http.ResponseWriter, body io.Reader, maxCaptureBytes int, onDataLine func(payload []byte)) (firstByte time.Time, captured []byte, err error) {
	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReaderSize(body, 32*1024)
	var capBuf bytes.Buffer
	captureEnabled := maxCaptureBytes > 0

	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if firstByte.IsZero() {
				firstByte = time.Now()
			}
			if _, writeErr := w.Write(line); writeErr != nil {
				return firstByte, nil, writeErr
			}
			if flusher != nil {
				flusher.Flush()
			}

			if captureEnabled {
				if capBuf.Len()+len(line) <= maxCaptureBytes {
					capBuf.Write(line)
				} else {
					captureEnabled = false
					capBuf.Reset()
				}
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
				if captureEnabled {
					captured = capBuf.Bytes()
				}
				return firstByte, captured, nil
			}
			return firstByte, nil, readErr
		}
	}
}

// replaySSE replays recorded raw SSE bytes line-by-line to w with flushing.
func replaySSE(w http.ResponseWriter, recorded []byte) {
	flusher, _ := w.(http.Flusher)
	reader := bufio.NewReader(bytes.NewReader(recorded))
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			_, _ = w.Write(line)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
}

// forwardSSETransform reads upstream SSE data lines without forwarding raw bytes,
// invoking onDataLine for each non-[DONE] payload so callers can emit translated events.
func forwardSSETransform(body io.Reader, onDataLine func(payload []byte)) (firstByte time.Time, err error) {
	reader := bufio.NewReaderSize(body, 32*1024)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmed, []byte("data:")) && onDataLine != nil {
				payload := bytes.TrimSpace(bytes.TrimPrefix(trimmed, []byte("data:")))
				if len(payload) > 0 && !bytes.Equal(payload, []byte("[DONE]")) {
					if firstByte.IsZero() {
						firstByte = time.Now()
					}
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
