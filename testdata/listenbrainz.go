//go:build !wasip1

package testdata

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"

	. "github.com/onsi/gomega"
)

func MakeLbzRequest(url, token string, body []byte) host.HTTPRequest {
	headers := map[string]string{
		"Accept":     "application/json",
		"User-Agent": "NavidromePlaylistImporter/6.0.0 (https://github.com/kgarner7/navidrome-listenbrainz-daily-playlist)",
	}

	if token != "" {
		headers["Authorization"] = "Token " + token
	}

	request := host.HTTPRequest{
		URL:       url,
		Headers:   headers,
		TimeoutMs: 20000,
	}

	if body == nil {
		request.Method = "GET"
	} else {
		request.Method = "POST"
		request.Body = body
		request.Headers["Content-Type"] = "application/json"
	}

	return request
}

func MakeLbzResponse(code int, path string, err error, rateLimited bool) (*host.HTTPResponse, error) {
	if err != nil {
		return nil, err
	}

	_, filename, _, ok := runtime.Caller(0)
	Expect(ok).To(BeTrue(), "unable to get the current filename")

	path = filepath.Join(filepath.Dir(filename), "listenbrainz", path)
	f, err := os.ReadFile(path)
	Expect(err).To(BeNil())

	resp := host.HTTPResponse{StatusCode: int32(code), Body: f}
	if rateLimited {
		resp.Headers = map[string]string{"x-ratelimit-remaining": "1", "x-ratelimit-reset-in": "5"}
	} else {
		resp.Headers = map[string]string{"x-ratelimit-remaining": "29", "x-ratelimit-reset-in": "10"}
	}

	return &resp, nil
}
