package transport

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strings"
)

func trafficCategory(path string) string {
	switch path {
	case "/api/v1/events":
		return "events"
	case "/api/v1/events/bodies":
		return "bodies"
	case "/api/v1/snapshot":
		return "snapshot"
	case "/api/v1/read":
		return "reads"
	case "/api/v1/adapters/index":
		return "adapter_index"
	case "/api/v1/adapters/status":
		return "adapter_status"
	case "/api/v1/client-config":
		return "discovery"
	case "/healthz":
		return "health"
	}
	if strings.HasPrefix(path, "/api/v1/adapters/packages/") {
		return "adapter_package"
	}
	return "control"
}

type countedRead struct {
	io.ReadCloser
	category string
	sent     bool
}

func (r *countedRead) Read(p []byte) (int, error) {
	n, e := r.ReadCloser.Read(p)
	if r.sent {
		RecordTraffic(r.category, uint64(n), 0, false)
	} else {
		RecordTraffic(r.category, 0, uint64(n), false)
	}
	return n, e
}

type measuredTransport struct{ base http.RoundTripper }

func (m measuredTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	category := "http_client/" + trafficCategory(request.URL.Path)
	RecordTraffic(category, 0, 0, true)
	copy := request.Clone(request.Context())
	if request.Body != nil {
		copy.Body = &countedRead{ReadCloser: request.Body, category: category, sent: true}
	}
	response, err := m.base.RoundTrip(copy)
	if response != nil {
		response.Body = &countedRead{ReadCloser: response.Body, category: category}
	}
	return response, err
}

var measuredDefault = measuredTransport{base: http.DefaultTransport}

type measuredResponse struct {
	http.ResponseWriter
	category string
}

func (m *measuredResponse) Write(p []byte) (int, error) {
	n, e := m.ResponseWriter.Write(p)
	RecordTraffic(m.category, uint64(n), 0, false)
	return n, e
}
func (m *measuredResponse) Unwrap() http.ResponseWriter { return m.ResponseWriter }
func (m *measuredResponse) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return m.ResponseWriter.(http.Hijacker).Hijack()
}
func (m *measuredResponse) Flush() {
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func MeasureHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			next.ServeHTTP(w, r)
			return
		}
		category := "http_server/" + trafficCategory(r.URL.Path)
		RecordTraffic(category, 0, 0, true)
		if r.Body != nil {
			r.Body = &countedRead{ReadCloser: r.Body, category: category}
		}
		next.ServeHTTP(&measuredResponse{ResponseWriter: w, category: category}, r)
	})
}
