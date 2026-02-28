//go:build !wasip1

package subsonic

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"

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
		url += "?u=" + user
		f, err := os.ReadFile("testdata/" + path + ".json")
		if err != nil {
			panic(err)
		}
		if values != nil {
			url += "&" + values.Encode()
		}
		host.SubsonicAPIMock.On("Call", "/rest/"+url).Return(string(f), nil)
	}

	Describe("MakeSubsonicRequest", func() {
		It("Handles an error returned from subsonic", func() {
			host.SubsonicAPIMock.On("Call", "/rest/ping?u=test").Return("", errors.New("fetch failed"))

			resp, ok := Call("ping", user, nil)
			Expect(resp).To(BeNil())
			Expect(ok).To(BeFalse())
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(1))

			validateCalls()
		})

		It("handles malformed json returned from application", func() {
			host.SubsonicAPIMock.On("Call", "/rest/ping?u=test").Return("{", nil)

			resp, ok := Call("ping", user, nil)
			Expect(resp).To(BeNil())
			Expect(ok).To(BeFalse())

			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(1))
			validateCalls()
		})

		It("handles error request", func() {
			mockSubsonicResponse("ping", nil, "error")
			resp, ok := Call("ping", user, nil)
			Expect(resp).To(BeNil())
			Expect(ok).To(BeFalse())

			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(1))
			validateCalls()
		})

		It("handles successful request", func() {
			mockSubsonicResponse("ping", nil, "ping.success")
			resp, ok := Call("ping", user, nil)
			Expect(resp).To(BeComparableTo(&responses.JsonWrapper{
				Subsonic: responses.Subsonic{
					Status:        "ok",
					Version:       "1.16.1",
					Type:          "navidrome",
					ServerVersion: "0.60.3",
					OpenSubsonic:  true,
				},
			}))
			Expect(ok).To(BeTrue())
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(1))
			validateCalls()
		})
	})

	Describe("LookupTrack", func() {
		const (
			mbid       = "9980309d-3480-4e7e-89ce-fce971a452be"
			title      = "world.execute(me);"
			artistMbid = "d2a92ee2-27ce-4e71-bfc5-12e34fe8ef56"
		)

		trackParams := url.Values{
			"artistCount": []string{"0"},
			"albumCount":  []string{"0"},
			"songCount":   []string{"1"},
			"query":       []string{mbid},
		}

		artistMbids := []string{artistMbid}

		artistParams := url.Values{
			"artistCount": []string{"1"},
			"albumCount":  []string{"0"},
			"songCount":   []string{"0"},
			"query":       []string{artistMbid},
		}

		fallbackParams := url.Values{
			"artistCount": []string{"0"},
			"albumCount":  []string{"0"},
			"songCount":   []string{"15"},
			"query":       []string{title},
		}

		var s *SubsonicHandler

		BeforeEach(func() {
			s = NewSubsonicHandler(15)
		})

		It("gives up if an error occurs on fetch by mbid", func() {
			mockSubsonicResponse("search3", &trackParams, "error")
			track := s.LookupTrack(user, title, mbid, artistMbids)
			Expect(track).To(BeNil())
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(1))
			validateCalls()
		})

		It("responds when track is found by mbid", func() {
			mockSubsonicResponse("search3", &trackParams, "child")
			track := s.LookupTrack(user, title, mbid, artistMbids)

			f, _ := os.ReadFile("testdata/child.json")
			var wrapper responses.JsonWrapper
			_ = json.Unmarshal(f, &wrapper)

			Expect(track).To(BeComparableTo(&wrapper.Subsonic.SearchResult3.Song[0]))
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(1))
			validateCalls()
		})

		It("gives up when track not found by mbid and artist not found by mbid", func() {
			mockSubsonicResponse("search3", &trackParams, "emptySearch")
			mockSubsonicResponse("search3", &artistParams, "emptySearch")

			track := s.LookupTrack(user, title, mbid, artistMbids)
			Expect(track).To(BeNil())
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(2))
			validateCalls()

			Expect(s.artistMbidToId).To(HaveKeyWithValue(artistMbid, ""))
		})

		It("gives up when track not found by mbid and artist search returns an error", func() {
			mockSubsonicResponse("search3", &trackParams, "emptySearch")
			mockSubsonicResponse("search3", &artistParams, "error")

			track := s.LookupTrack(user, title, mbid, artistMbids)
			Expect(track).To(BeNil())
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(2))
			validateCalls()

			Expect(s.artistMbidToId).To(BeEmpty())
		})

		It("looks up track by title if artist found by mbid, but still not found", func() {
			mockSubsonicResponse("search3", &trackParams, "emptySearch")
			mockSubsonicResponse("search3", &artistParams, "artistSearch")
			mockSubsonicResponse("search3", &fallbackParams, "emptySearch")

			track := s.LookupTrack(user, title, mbid, artistMbids)
			Expect(track).To(BeNil())
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(3))
			validateCalls()

			Expect(s.artistMbidToId).To(HaveKeyWithValue(artistMbid, "2fURvRfCF5WaU1262xTQLp"))
		})

		It("looks up track by title if artist found by mbid, and last search errors", func() {
			mockSubsonicResponse("search3", &trackParams, "emptySearch")
			mockSubsonicResponse("search3", &artistParams, "artistSearch")
			mockSubsonicResponse("search3", &fallbackParams, "error")

			track := s.LookupTrack(user, title, mbid, artistMbids)
			Expect(track).To(BeNil())
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(3))
			validateCalls()

			Expect(s.artistMbidToId).To(HaveKeyWithValue(artistMbid, "2fURvRfCF5WaU1262xTQLp"))
		})

		It("looks up track by title if artist found by mbid, and is found", func() {
			mockSubsonicResponse("search3", &trackParams, "emptySearch")
			mockSubsonicResponse("search3", &artistParams, "artistSearch")
			mockSubsonicResponse("search3", &fallbackParams, "child")

			track := s.LookupTrack(user, title, mbid, artistMbids)

			f, _ := os.ReadFile("testdata/child.json")
			var wrapper responses.JsonWrapper
			_ = json.Unmarshal(f, &wrapper)

			Expect(track).To(BeComparableTo(&wrapper.Subsonic.SearchResult3.Song[0]))
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(3))
			validateCalls()

			Expect(s.artistMbidToId).To(HaveKeyWithValue(artistMbid, "2fURvRfCF5WaU1262xTQLp"))
		})
	})

	Describe("UpdatePlaylist", func() {
		title := "Generated Daily Jams"
		songIds := []string{"cd020be4e71f3f9a1856ebc89741f4d9"}
		playlistId := "C8hOrsjiVnnHZTXqxLs57t"

		It("errors if playlists cannot be fetched", func() {
			mockSubsonicResponse("getPlaylists", &url.Values{"username": []string{user}}, "error")

			err := UpdatePlaylist(user, title, "This is a comment", songIds)
			Expect(err).To(MatchError("Failed to fetch subsonic playlists for user test"))
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(1))
			validateCalls()
		})

		It("errors if playlist cannot be created", func() {
			mockSubsonicResponse("getPlaylists", &url.Values{"username": []string{user}}, "noPlaylists")
			mockSubsonicResponse("createPlaylist", &url.Values{"name": []string{title}, "songId": songIds}, "error")

			err := UpdatePlaylist(user, title, "This is a comment", songIds)
			Expect(err).To(MatchError("Failed to create playlist " + title))
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(2))
			validateCalls()
		})

		It("errors if unable to update playlist", func() {
			mockSubsonicResponse("getPlaylists", &url.Values{"username": []string{user}}, "noPlaylists")
			mockSubsonicResponse("createPlaylist", &url.Values{"name": []string{title}, "songId": songIds}, "createPlaylist")
			mockSubsonicResponse("updatePlaylist", &url.Values{"playlistId": []string{playlistId}, "comment": []string{"this is a comment"}}, "error")

			err := UpdatePlaylist(user, title, "this is a comment", songIds)
			Expect(err).To(MatchError("Failed to update playlist " + title + " for " + user))
			Expect(host.SubsonicAPIMock.Calls).To(HaveLen(3))
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
