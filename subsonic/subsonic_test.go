//go:build !wasip1

package subsonic

import (
	"errors"
	"fmt"
	"listenbrainz-daily-playlist/retry"
	"listenbrainz-daily-playlist/testdata"
	"net/url"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("", func() {
	const user = "test"

	BeforeEach(func() {
		pdk.ResetMock()
		pdk.PDKMock.Calls = nil
		pdk.PDKMock.ExpectedCalls = nil
		host.SubsonicAPIMock.ExpectedCalls = nil
		host.SubsonicAPIMock.Calls = nil
		pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()
	})

	validateCalls := func() {
		expectedCalls := []mock.Arguments{}
		for _, call := range pdk.PDKMock.ExpectedCalls {
			if call.Method != "Log" {
				expectedCalls = append(expectedCalls, call.Arguments)
			}
		}

		actualCalls := []mock.Arguments{}
		for _, call := range pdk.PDKMock.Calls {
			if call.Method != "Log" {
				actualCalls = append(actualCalls, call.Arguments)
			}
		}

		Expect(expectedCalls).To(Equal(actualCalls))

		expectedCalls = []mock.Arguments{}
		for _, call := range host.SubsonicAPIMock.ExpectedCalls {
			if call.Method != "Log" {
				expectedCalls = append(expectedCalls, call.Arguments)
			}
		}

		actualCalls = []mock.Arguments{}
		for _, call := range host.SubsonicAPIMock.Calls {
			if call.Method != "Log" {
				actualCalls = append(actualCalls, call.Arguments)
			}
		}

		Expect(expectedCalls).To(Equal(actualCalls))
	}

	mockSubsonicResponse := func(url string, values *url.Values, path string) {
		testdata.MockSubsonicResponse(user, url, values, path)
	}

	Describe("MakeSubsonicRequest", func() {
		It("Handles an error returned from subsonic", func() {
			host.SubsonicAPIMock.On("Call", "/rest/ping?u=test").Return("", errors.New("fetch failed"))

			resp, err := Call("ping", user, nil)
			Expect(resp).To(BeNil())
			Expect(err).To(Equal(retry.FatalError("fetch failed")))
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(1))

			validateCalls()
		})

		It("handles malformed json returned from application", func() {
			host.SubsonicAPIMock.On("Call", "/rest/ping?u=test").Return("{", nil)

			resp, err := Call("ping", user, nil)
			Expect(resp).To(BeNil())
			Expect(err.Retryable).To(BeFalse())
			Expect(err.Error).ToNot(BeNil())

			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(1))
			validateCalls()
		})

		It("handles error request", func() {
			mockSubsonicResponse("ping", nil, "error")
			resp, err := Call("ping", user, nil)
			Expect(resp).To(BeNil())
			Expect(err).To(Equal(retry.FatalError("subsonic status is not ok: (40) Wrong username or password")))
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(1))
			validateCalls()
		})

		It("handles internal server error (retryable error)", func() {
			mockSubsonicResponse("ping", nil, "errorRetryable")
			resp, err := Call("ping", user, nil)
			Expect(resp).To(BeNil())
			Expect(err).To(Equal(&retry.Error{
				Error:     fmt.Errorf("subsonic status is not ok: (0) Internal server error: unknown"),
				Retryable: true,
			}))

			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(1))
			validateCalls()
		})

		It("handles successful request", func() {
			mockSubsonicResponse("ping", nil, "ping.success")
			resp, err := Call("ping", user, nil)
			Expect(resp).To(BeComparableTo(&responses.JsonWrapper{
				Subsonic: responses.Subsonic{
					Status:        "ok",
					Version:       "1.16.1",
					Type:          "navidrome",
					ServerVersion: "0.60.3",
					OpenSubsonic:  true,
				},
			}))
			Expect(err).To(BeNil())
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(1))
			validateCalls()
		})
	})

	Describe("UpdatePlaylist", func() {
		title := "Generated Daily Jams"
		songIds := []string{"cd020be4e71f3f9a1856ebc89741f4d9"}
		playlistId := "C8hOrsjiVnnHZTXqxLs57t"

		It("errors if playlists cannot be fetched", func() {
			mockSubsonicResponse("getPlaylists", &url.Values{"username": []string{user}}, "error")

			err := UpdatePlaylist(user, title, "This is a comment", songIds)
			Expect(err).To(Equal(retry.FatalError("subsonic status is not ok: (40) Wrong username or password")))
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(1))
			validateCalls()
		})

		It("errors if playlist cannot be created", func() {
			mockSubsonicResponse("getPlaylists", &url.Values{"username": []string{user}}, "noPlaylists")
			mockSubsonicResponse("createPlaylist", &url.Values{"name": []string{title}, "songId": songIds}, "error")

			err := UpdatePlaylist(user, title, "This is a comment", songIds)
			Expect(err).To(Equal(retry.FatalError("subsonic status is not ok: (40) Wrong username or password")))
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(2))
			validateCalls()
		})

		It("errors if unable to update playlist", func() {
			mockSubsonicResponse("getPlaylists", &url.Values{"username": []string{user}}, "noPlaylists")
			mockSubsonicResponse("createPlaylist", &url.Values{"name": []string{title}, "songId": songIds}, "createPlaylist")
			mockSubsonicResponse("updatePlaylist", &url.Values{"playlistId": []string{playlistId}, "comment": []string{"this is a comment"}}, "error")

			err := UpdatePlaylist(user, title, "this is a comment", songIds)
			Expect(err).To(Equal(retry.FatalError("subsonic status is not ok: (40) Wrong username or password")))
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(3))
			validateCalls()
		})

		It("Errors in a retryable way if an error occurs", func() {
			mockSubsonicResponse("getPlaylists", &url.Values{"username": []string{user}}, "errorRetryable")

			err := UpdatePlaylist(user, title, "This is a comment", songIds)
			Expect(err).To(Equal(&retry.Error{
				Error:     fmt.Errorf("subsonic status is not ok: (0) Internal server error: unknown"),
				Retryable: true,
			}))
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(1))
			validateCalls()
		})

		It("creates a new playlist when no playlist exists", func() {
			mockSubsonicResponse("getPlaylists", &url.Values{"username": []string{user}}, "noPlaylists")
			mockSubsonicResponse("createPlaylist", &url.Values{"name": []string{title}, "songId": songIds}, "createPlaylist")
			mockSubsonicResponse("updatePlaylist", &url.Values{"playlistId": []string{playlistId}, "comment": []string{"this is a comment"}}, "ping.success")

			err := UpdatePlaylist(user, title, "this is a comment", songIds)
			Expect(err).To(BeNil())
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(3))
			validateCalls()
		})

		It("updates an existing playlist but does not change comment", func() {
			mockSubsonicResponse("getPlaylists", &url.Values{"username": []string{user}}, "existingPlaylists")
			mockSubsonicResponse("createPlaylist", &url.Values{"playlistId": []string{playlistId}, "songId": songIds}, "createPlaylist")

			err := UpdatePlaylist(user, title, "This is a comment", songIds)
			Expect(err).To(BeNil())
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(2))
			validateCalls()
		})
	})
})
