package main

import (
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"log/slog"
)

// Hop-by-hop headers. These are removed when sent to the backend.
// http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
var hopHeaders = []string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te", // canonicalized version of "TE"
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

func delHopHeaders(header http.Header) {
	for _, h := range hopHeaders {
		header.Del(h)
	}
}

func appendHostToXForwardHeader(header http.Header, host string) {
	// If we aren't the first proxy retain prior
	// X-Forwarded-For information as a comma+space
	// separated list and fold multiple headers into one.
	if prior, ok := header["X-Forwarded-For"]; ok {
		host = strings.Join(prior, ", ") + ", " + host
	}
	header.Set("X-Forwarded-For", host)
}

type proxy struct {
}

func (p *proxy) ServeHTTP(wr http.ResponseWriter, req *http.Request) {
	log := slog.With("remote", req.RemoteAddr, "method", req.Method, "URL", req.URL)
	log.Info("Incoming Request")

	if strings.ToUpper(req.Method) == "CONNECT" {
		clientConn, _, _ := wr.(http.Hijacker).Hijack()

		var (
			sock net.Conn
			err  error
		)
		if req.URL.Port() == "" {
			sock, err = net.Dial("tcp", req.URL.Hostname()+":80")
		} else {
			sock, err = net.Dial("tcp", req.URL.Host)
		}

		if err != nil {
			fmt.Fprintf(clientConn, "HTTP/1.1 502 Bad Gateway\n\n")
			clientConn.Close()
			return
		}

		fmt.Fprintf(clientConn, "HTTP/1.1 200 Connection Established\n\n")

		go io.Copy(clientConn, sock)
		go io.Copy(sock, clientConn)

		return
	}

	client := &http.Client{}

	//http: Request.RequestURI can't be set in client requests.
	//http://golang.org/src/pkg/net/http/client.go
	req.RequestURI = ""

	delHopHeaders(req.Header)

	if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		appendHostToXForwardHeader(req.Header, clientIP)
	}

	resp, err := client.Do(req)
	if err != nil {
		http.Error(wr, "Server Error performing request", http.StatusInternalServerError)
		log.Error("client request failed", "error", err)
		return
	}
	defer resp.Body.Close()

	log.Info("Response", "status", resp.Status)

	delHopHeaders(resp.Header)

	copyHeader(wr.Header(), resp.Header)
	wr.WriteHeader(resp.StatusCode)
	io.Copy(wr, resp.Body)
}

func main() {
	logHandler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == "time" {
				// a.Value = slog.StringValue(time.Now().Format(time.RFC3339))
				a.Value = slog.TimeValue(time.Now())
			}
			return a
		},
	})

	slog.SetDefault(slog.New(logHandler))

	var addr = flag.String("addr", "127.0.0.1:8080", "The addr of the application.")
	flag.Parse()

	handler := &proxy{}

	slog.Info("Starting proxy", "listen", *addr)
	if err := http.ListenAndServe(*addr, handler); err != nil {
		slog.Error("ListenAndServe (quiting)", "error", err)
		return
	}
}
