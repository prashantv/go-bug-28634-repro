package main

import (
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

// Handler supports 2 functions:
// drop: reads a few bytes from the body, then closes the request body
// echo: echoes the request body
func testServerHandler(w http.ResponseWriter, r *http.Request) {
	var err error
	switch r.URL.Path {
	case "/drop":
		// Close the body to trigger https://github.com/golang/go/issues/28634
		err = r.Body.Close()
	default:
		// Echo otherwise
		var body []byte
		body, err = ioutil.ReadAll(r.Body)
		if err != nil {
			break
		}

		_, err = w.Write(body)
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func TestSendLargeUnreadPayload(t *testing.T) {
	ln, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err, "failed to listen")

	server := newHTTP2Server(http.HandlerFunc(testServerHandler))
	url := fmt.Sprintf("http://%v/", ln.Addr())
	defer server.Close()
	go server.Serve(ln)

	// We want a large request body to consume the connection flow control window.
	var largePayload = strings.Repeat("ABCDEFGH", 1024*1024)

	client := newHTTP2Client()
	for i := 0; i < 100; i++ {
		t.Logf("Running iteration %v", i)

		// First, make a large payload requests which will not be fully read
		// triggering https://github.com/golang/go/issues/28634
		// This will consume some of the connection flow control window.
		// Once it's fully consumed, the echo below will fail.
		_, err = client.Post(url+"drop", "application/raw", strings.NewReader(largePayload))
		require.NoError(t, err, "POST failed")

		// Ensure that we can still do a small echo request/response.
		echoTest(t, client, url)
	}
}

func echoTest(t *testing.T, client *http.Client, url string) {
	const data = `{"hello": "world"}`
	res, err := client.Post(url, "application/json", strings.NewReader(data))
	require.NoError(t, err, "echo: POST failed")

	got, err := ioutil.ReadAll(res.Body)
	require.NoError(t, err, "echo: failed to read response body")
	assert.Equal(t, http.StatusOK, res.StatusCode, "echo: unexpected response code")
	assert.Equal(t, data, string(got), "echo: unexpected response")
}

func newHTTP2Server(delegate http.Handler) *http.Server {
	return &http.Server{
		Handler: h2c.NewHandler(delegate, &http2.Server{}),
	}
}

func newHTTP2Client() *http.Client {
	return &http.Client{
		Transport: &http2.Transport{
			AllowHTTP: true,
			DialTLS: func(network, addr string, _ *tls.Config) (net.Conn, error) {
				return net.Dial(network, addr)
			},
		},
		Timeout: time.Second,
	}
}
