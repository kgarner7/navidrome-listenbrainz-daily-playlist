//go:build !wasip1

package dispatcher

import (
	"encoding/json"
	"os"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/server/subsonic/responses"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("Dispatcher", func() {
	BeforeEach(func() {
		pdk.ResetMock()
		pdk.PDKMock.Calls = nil
		pdk.PDKMock.ExpectedCalls = nil
		host.SubsonicAPIMock.Calls = nil
		host.SubsonicAPIMock.ExpectedCalls = nil
		host.TaskMock.Calls = nil
		host.TaskMock.ExpectedCalls = nil
		pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()
	})

	mockUserConfig := func(path string) {
		f, err := os.ReadFile("testdata/" + path + ".json")
		if err != nil {
			panic(err)
		}
		pdk.PDKMock.On("GetConfig", "users").Return(string(f), true)
	}

	Describe("parseRatings", func() {
		It("should gracefully handle nil", func() {
			ratings := parseRatings(nil)
			Expect(ratings).To(Equal(map[int32]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true}))
		})

		It("should parse all rating levels", func() {
			ratings := parseRatings([]string{"0", "1", "2", "3", "4", "5"})
			Expect(ratings).To(Equal(map[int32]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true}))
		})

		It("should exclude ratings when asked", func() {
			ratings := parseRatings([]string{"0", "2", "3", "4", "5"})
			Expect(ratings).To(Equal(map[int32]bool{0: true, 2: true, 3: true, 4: true, 5: true}))
		})

		It("should exclude other ratings when asked", func() {
			ratings := parseRatings([]string{"1"})
			Expect(ratings).To(Equal(map[int32]bool{1: true}))
		})

		It("should allow duplicate ratings", func() {
			ratings := parseRatings([]string{"1", "1", "5"})
			Expect(ratings).To(Equal(map[int32]bool{1: true, 5: true}))
		})

		It("should ignore invalid rating values", func() {
			ratings := parseRatings([]string{"1", "1", "5", "6", "-1", "bad"})
			Expect(ratings).To(Equal(map[int32]bool{1: true, 5: true}))
		})
	})

	Describe("GetConfig", func() {

		DescribeTable("errors", func(path, error string) {
			mockUserConfig(path)
			users, fallback, err := GetConfig()
			Expect(users).To(BeNil())
			Expect(fallback).To(BeZero())
			Expect(err).To(MatchError(error))
		},
			Entry(
				"should reject a config where a user doesn't have a username",
				"userConfig.singleUser.missingNDUsername",
				"user must have a Navidrome username and ListenBrainz username",
			),
			Entry(
				"should reject a config which has duplicate names in the source patch",
				"userConfig.duplicateSourceName",
				"duplicate playlist name found: playlist name",
			),
			Entry(
				"should reject a config where a source patch and generated name clash",
				"userConfig.duplicatePatchAndGenerated",
				"duplicate playlist name found: playlist name 2",
			),
			Entry(
				"should reject a config where a source patch and playlist name clash",
				"userConfig.duplicatePatchAndImport",
				"duplicate playlist name found: weekly name",
			),
		)

		It("should reject a config missing key users", func() {
			pdk.PDKMock.On("GetConfig", "users").Return("", false)
			users, fallback, err := GetConfig()
			Expect(users).To(BeNil())
			Expect(fallback).To(BeZero())
			Expect(err).To(MatchError("missing required 'users' configuration"))
		})

		It("return a good user config, no fallback", func() {
			mockUserConfig("userConfig.complete")
			pdk.PDKMock.On("GetConfig", "fallbackCount").Return("", false)

			users, fallback, err := GetConfig()
			Expect(users).To(Equal([]userConfig{
				{
					GeneratePlaylist:             true,
					GeneratedPlaylist:            "Generated Daily Jams",
					GeneratedPlaylistTrackAge:    60,
					GeneratedPlaylistArtistLimit: 15,
					NDUsername:                   "username",
					LbzUsername:                  "lbz username",
					LbzToken:                     "1234",
					Ratings:                      []string{"0", "2", "3", "4", "5"},
					Sources: []source{
						{
							SourcePatch:  "daily-jams",
							PlaylistName: "playlist name",
						},
						{
							SourcePatch:  "weekly-jams",
							PlaylistName: "weekly name",
						},
					},
					Playlists: []playlist{
						{Name: "1234", LbzId: "0", OneTime: false},
					},
				},
			}))
			Expect(fallback).To(Equal(15))
			Expect(err).To(BeNil())
		})

		DescribeTable("returns a good config, but invalid fallback values", func(count, error string) {
			mockUserConfig("userConfig.complete")
			pdk.PDKMock.On("GetConfig", "fallbackCount").Return(count, true)

			users, fallback, err := GetConfig()
			Expect(users).To(BeNil())
			Expect(fallback).To(Equal(0))
			Expect(err).To(MatchError(error))
		},
			Entry("non-integer", "abcd", "fallbackCount is not a valid number"),
			Entry("zero", "0", "fallbackCount must be between 1 and 500 (inclusive)"),
			Entry("big number", "501", "fallbackCount must be between 1 and 500 (inclusive)"),
		)
	})

	Describe("InitialFetch", func() {
		ratings := map[int32]bool{int32(0): true, int32(2): true, int32(3): true, int32(4): true, int32(5): true}
		now := func() *time.Time {
			t := time.Now()
			return &t
		}

		DescribeTable("dispatch rules", func(daily *time.Time, weekly *time.Time, generated *time.Time, imported *time.Time, log string) {
			mockUserConfig("userConfig.complete")
			pdk.PDKMock.On("GetConfig", "fallbackCount").Return("", false)

			now := time.Now()
			playlists := []responses.Playlist{}

			var fetchPayload []byte = nil
			var generatePayload []byte = nil
			var importPayload []byte = nil
			var err error

			sources := []source{}

			if daily != nil {
				playlists = append(playlists, responses.Playlist{
					Id:      "12",
					Name:    "playlist name",
					Changed: *daily,
				})
			}

			if daily == nil || now.Sub(*daily) > 3*time.Hour {
				sources = append(sources, source{SourcePatch: "daily-jams", PlaylistName: "playlist name"})
			}

			if weekly != nil {
				playlists = append(playlists, responses.Playlist{
					Id:      "34",
					Name:    "weekly name",
					Changed: *weekly,
				})
			}

			if weekly == nil || now.Sub(*weekly) > 3*time.Hour {
				sources = append(sources, source{SourcePatch: "weekly-jams", PlaylistName: "weekly name"})
			}

			if len(sources) > 0 {
				j := Job{
					JobType:     FetchPatches,
					Username:    "username",
					LbzUsername: "lbz username",
					LbzToken:    "1234",
					Ratings:     ratings,
					Patch:       &patchJob{Sources: sources},
					Fallback:    15,
				}

				fetchPayload, err = json.Marshal(j)
				Expect(err).To(BeNil())
				host.TaskMock.On("Enqueue", "job-queue", fetchPayload).Return("", nil)
			}

			if generated != nil {
				playlists = append(playlists, responses.Playlist{
					Id:      "56",
					Name:    "Generated Daily Jams",
					Changed: *generated,
				})
			}

			if generated == nil || now.Sub(*generated) > 3*time.Hour {
				j := Job{
					JobType:     GenerateJams,
					Username:    "username",
					LbzUsername: "lbz username",
					LbzToken:    "1234",
					Ratings:     ratings,
					Generate: &generationJob{
						Name:        "Generated Daily Jams",
						TrackAge:    60,
						ArtistLimit: 15,
					},
					Fallback: 15,
				}

				generatePayload, err = json.Marshal(j)
				Expect(err).To(BeNil())
				host.TaskMock.On("Enqueue", "job-queue", generatePayload).Return("", nil)
			}

			if imported != nil {
				playlists = append(playlists, responses.Playlist{
					Id:      "56",
					Name:    "1234",
					Changed: *imported,
				})
			}

			if imported == nil || now.Sub(*imported) > 3*time.Hour {
				j := Job{
					JobType:     ImportPlaylist,
					Username:    "username",
					LbzUsername: "lbz username",
					LbzToken:    "1234",
					Ratings:     ratings,
					Import:      &importJob{Name: "1234", LbzId: "0"},
					Fallback:    15,
				}

				importPayload, err = json.Marshal(j)
				Expect(err).To(BeNil())
				host.TaskMock.On("Enqueue", "job-queue", importPayload).Return("", nil)
			}

			resp := responses.JsonWrapper{
				Subsonic: responses.Subsonic{
					Status:        "ok",
					Version:       "1.16.1",
					Type:          "navidrome",
					ServerVersion: "0.60.3",
					OpenSubsonic:  true,
					Playlists:     &responses.Playlists{Playlist: playlists},
				},
			}

			payload, err := json.Marshal(resp)
			Expect(err).To(BeNil())
			host.SubsonicAPIMock.On("Call", "/rest/getPlaylists?u=username&username=username").Return(string(payload), nil)

			err = InitialFetch()
			Expect(err).To(BeNil())

			if fetchPayload != nil {
				host.TaskMock.AssertCalled(GinkgoT(), "Enqueue", "job-queue", fetchPayload)
			}

			if generatePayload != nil {
				host.TaskMock.AssertCalled(GinkgoT(), "Enqueue", "job-queue", generatePayload)
			}
			if importPayload != nil {
				host.TaskMock.AssertCalled(GinkgoT(), "Enqueue", "job-queue", importPayload)
			}

			pdk.PDKMock.AssertCalled(GinkgoT(), "Log", pdk.LogInfo, log)
		},
			Entry(
				"no playlists exist", nil, nil, nil, nil,
				"Missing or outdated playlists, fetching on initial sync. Missing: [User: `username`, Source: `playlist name` User: `username`, Source: `weekly name` User: `username`, Source: `Generated Daily Jams` User: `username`, Source: `1234`], Outdated: []",
			),
			Entry(
				"playlists exist but are all old", &time.Time{}, &time.Time{}, &time.Time{}, &time.Time{}, "Missing or outdated playlists, fetching on initial sync. Missing: [], Outdated: [User: `username`, Source: `playlist name` User: `username`, Source: `weekly name` User: `username`, Source: `Generated Daily Jams` User: `username`, Source: `1234`]",
			),
			Entry(
				"all playlists are recent", now(), now(), now(), now(),
				"No missing/outdated playlists, not fetching",
			),
			Entry(
				"imports with at least one source present", nil, now(), now(), &time.Time{},
				"Missing or outdated playlists, fetching on initial sync. Missing: [User: `username`, Source: `playlist name`], Outdated: [User: `username`, Source: `1234`]",
			),
		)

	})
})
