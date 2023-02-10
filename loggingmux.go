package main

import (
	"net/http"

	"go.uber.org/zap"
)

// LoggingMux is an http server mixin which logs HTTP requests.
type LoggingMux struct {
	*http.ServeMux
	logger *zap.Logger
}

// NewLoggingMux creates a new LoggingMux.
func NewLoggingMux(logger *zap.Logger) *LoggingMux {
	mux := http.NewServeMux()
	return &LoggingMux{ServeMux: mux, logger: logger}
}

func (lm *LoggingMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logger := lm.logger.With(zap.String("remote-addr", r.RemoteAddr), zap.String("request-uri", r.RequestURI), zap.String("method", r.Method))
	logger.Info("incoming request")
	loggingWriter := loggingResponseWriter{ResponseWriter: w}
	lm.ServeMux.ServeHTTP(&loggingWriter, r)
	logger.Info("request completed", zap.Int("bytes-written", loggingWriter.bytesWritten), zap.Int("status-code", loggingWriter.statusCode))
}

type loggingResponseWriter struct {
	http.ResponseWriter
	bytesWritten int
	statusCode   int
}

func (w *loggingResponseWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.bytesWritten += n
	return n, err
}

func (w *loggingResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}
