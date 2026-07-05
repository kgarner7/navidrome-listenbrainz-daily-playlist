//go:build !wasip1

package testdata

import (
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"

	. "github.com/onsi/gomega"
)

func MockSubsonicResponse(user string, url string, values *url.Values, path string) {
	_, filename, _, ok := runtime.Caller(0)
	Expect(ok).To(BeTrue(), "unable to get the current filename")

	url += "?u=" + user
	path = filepath.Join(filepath.Dir(filename), "subsonic", path+".json")
	f, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	if values != nil {
		url += "&" + values.Encode()
	}
	host.SubsonicAPIMock.On("Call", "/rest/"+url).Return(string(f), nil)
}
