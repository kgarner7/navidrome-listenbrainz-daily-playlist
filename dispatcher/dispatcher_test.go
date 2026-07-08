//go:build !wasip1

package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"listenbrainz-daily-playlist/retry"
	"listenbrainz-daily-playlist/sleep"
	"listenbrainz-daily-playlist/subsonic"
	"listenbrainz-daily-playlist/testdata"
	"maps"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/types"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/stretchr/testify/mock"
)

var _ = Describe("Dispatcher", func() {
	const EMPTY_UUID = "00000000-0000-0000-0000-000000000000"
	var (
		CONNECTION_RESET = errors.New("read tcp 8.8.8.8:60000->142.132.240.1:443: read: connection reset by peer")
		CONTEXT_DEADLINE = errors.New("Get \"https://api.listenbrainz.org/1/user/user/playlists/createdfor\": " + context.DeadlineExceeded.Error())
	)

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
			users, err := GetConfig()
			Expect(users).To(BeNil())
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
			users, err := GetConfig()
			Expect(users).To(BeNil())
			Expect(err).To(MatchError("missing required 'users' configuration"))
		})

		It("return a good user config, no fallback", func() {
			mockUserConfig("userConfig.complete")
			pdk.PDKMock.On("GetConfig").Return("", false)

			users, err := GetConfig()
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
			Expect(err).To(BeNil())
		})
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
			playlists := []subsonic.Playlist{}

			var fetchPayload []byte = nil
			var generatePayload []byte = nil
			var importPayload []byte = nil
			var err error

			sources := []source{}

			if daily != nil {
				playlists = append(playlists, subsonic.Playlist{
					Id:      "12",
					Name:    "playlist name",
					Changed: *daily,
				})
			}

			if daily == nil || now.Sub(*daily) > 3*time.Hour {
				sources = append(sources, source{SourcePatch: "daily-jams", PlaylistName: "playlist name"})
			}

			if weekly != nil {
				playlists = append(playlists, subsonic.Playlist{
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
				}

				fetchPayload, err = json.Marshal(j)
				Expect(err).To(BeNil())
				host.TaskMock.On("Enqueue", "job-queue", fetchPayload).Return("", nil)
			}

			if generated != nil {
				playlists = append(playlists, subsonic.Playlist{
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
				}

				generatePayload, err = json.Marshal(j)
				Expect(err).To(BeNil())
				host.TaskMock.On("Enqueue", "job-queue", generatePayload).Return("", nil)
			}

			if imported != nil {
				playlists = append(playlists, subsonic.Playlist{
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
				}

				importPayload, err = json.Marshal(j)
				Expect(err).To(BeNil())
				host.TaskMock.On("Enqueue", "job-queue", importPayload).Return("", nil)
			}

			resp := subsonic.JsonWrapper{
				Subsonic: subsonic.Subsonic{
					Status:    "ok",
					Playlists: &subsonic.Playlists{Playlist: playlists},
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

	Describe("ClearQueue", func() {
		It("should successfully clear queue", func() {
			host.TaskMock.On("ClearQueue", "job-queue").Return(int64(1), nil)
			ClearQueue()
			host.TaskMock.AssertCalled(GinkgoT(), "ClearQueue", "job-queue")
			pdk.PDKMock.AssertCalled(GinkgoT(), "Log", pdk.LogInfo, "Removed 1 job(s) from task queue")
		})

		It("should ignore clear error but log in error", func() {
			host.TaskMock.On("ClearQueue", "job-queue").Return(int64(0), errors.New("Error"))
			ClearQueue()
			host.TaskMock.AssertCalled(GinkgoT(), "ClearQueue", "job-queue")
			pdk.PDKMock.AssertCalled(GinkgoT(), "Log", pdk.LogError, "Failed to clear task queue: Error")
		})
	})

	Describe("dispatching", func() {
		const lbzEndpoint = "https://api.listenbrainz.org/1"
		var job Job

		mockSleep := func(d time.Duration) {}

		BeforeEach(func() {
			job = Job{Username: "username"}
			oldSleep := sleep.Sleep
			sleep.Sleep = mockSleep

			pdk.ResetMock()
			pdk.PDKMock.Calls = nil
			pdk.PDKMock.ExpectedCalls = nil
			host.HTTPMock.Calls = nil
			host.HTTPMock.ExpectedCalls = nil
			host.TaskMock.Calls = nil
			host.TaskMock.ExpectedCalls = nil
			host.MatcherMock.Calls = nil
			host.MatcherMock.ExpectedCalls = nil
			host.SubsonicAPIMock.Calls = nil
			host.SubsonicAPIMock.ExpectedCalls = nil
			pdk.PDKMock.On("Log", mock.Anything, mock.Anything).Maybe()

			DeferCleanup(func() {
				sleep.Sleep = oldSleep
			})
		})

		setupResponse := func(request host.HTTPRequest, code int, dataPath string, err error, rateLimited bool) {
			host.HTTPMock.On("Send", request).Return(testdata.MakeLbzResponse(code, dataPath+".json", err, rateLimited))
		}

		Describe("dispatchSourceFetching", func() {
			const URL = lbzEndpoint + "/user/test/playlists/createdfor"

			BeforeEach(func() {
				job.JobType = FetchPatches
			})

			It("should error if patch is missing", func() {
				err := job.Dispatch()
				Expect(err).To(Equal(retry.FatalError("attempting to dispatch patch fetch without patch")))
			})

			It("should fail if LBZ response is bad", func() {
				job.LbzUsername = "a"
				job.Patch = &patchJob{Sources: []source{{SourcePatch: "daily-jams", PlaylistName: "daily-jams"}}}

				url := lbzEndpoint + "/user/a/playlists/createdfor"
				request := testdata.MakeLbzRequest(url, "", nil)
				setupResponse(request, 404, "createdFor.noUser", nil, false)
				err := job.Dispatch()
				Expect(err).To(Equal(retry.FatalError("ListenBrainz HTTP Error. Code: 404, Error: Cannot find user: a")))
			})

			DescribeTable("should issue a retry if present", func(recoverable error) {
				job.LbzUsername = "a"
				job.Patch = &patchJob{Sources: []source{{SourcePatch: "daily-jams", PlaylistName: "daily-jams"}}}

				url := lbzEndpoint + "/user/a/playlists/createdfor"
				request := testdata.MakeLbzRequest(url, "", nil)
				setupResponse(request, 0, "", recoverable, false)
				err := job.Dispatch()
				Expect(err).To(Equal(retry.TempError(recoverable)))
			},
				Entry("connection reset", CONNECTION_RESET),
				Entry("context deadline", CONTEXT_DEADLINE),
			)

			It("should error if no playlist found", func() {
				job.LbzUsername = "test"
				job.Patch = &patchJob{Sources: []source{{SourcePatch: "daily-jams", PlaylistName: "daily-jams"}}}

				request := testdata.MakeLbzRequest(URL, "", nil)
				setupResponse(request, 200, "createdFor.success", nil, false)
				err := job.Dispatch()

				Expect(err).ToNot(BeNil())
				Expect(err.Retryable).To(BeFalse())
				Expect(err.Error.Error()).To(Equal("no playlist for ListenBrainz user `test` found with algorithm/source patch `daily-jams`"))

				Expect(host.TaskMock.Calls).To(BeNil())
			})

			It("should find real playlist, retry on enqueue error", func() {
				job.LbzUsername = "test"
				job.Patch = &patchJob{Sources: []source{{SourcePatch: "weekly-exploration", PlaylistName: "daily-jams"}}}

				request := testdata.MakeLbzRequest(URL, "", nil)
				setupResponse(request, 200, "createdFor.success", nil, false)

				dispatched := Job{
					JobType:     ImportPlaylist,
					Username:    "username",
					LbzUsername: "test",
					Import: &importJob{
						Name:  "daily-jams",
						LbzId: EMPTY_UUID,
					},
				}

				dispatchPayload, marshalErr := json.Marshal(dispatched)
				Expect(marshalErr).To(BeNil())
				host.TaskMock.On("Enqueue", "job-queue", dispatchPayload).Return("", errors.New("nope"))

				err := job.Dispatch()
				Expect(err).To(Equal(retry.TempError(errors.New("nope"))))
				Expect(host.TaskMock.Calls).To(HaveLen(1))
			})

			It("should find real playlist, succeed on shipping task, full job", func() {
				job.LbzUsername = "test"
				job.Patch = &patchJob{Sources: []source{
					{SourcePatch: "weekly-exploration", PlaylistName: "weekly exploration"},
					{SourcePatch: "daily-jams", PlaylistName: "daily jams"},
				}}
				job.LbzToken = "1234"
				job.Ratings = map[int32]bool{int32(5): true}

				url := lbzEndpoint + "/user/test/playlists/createdfor"
				request := testdata.MakeLbzRequest(url, "1234", nil)
				setupResponse(request, 200, "createdFor.success", nil, false)

				dispatched := Job{
					JobType:     ImportPlaylist,
					Username:    "username",
					LbzUsername: "test",
					LbzToken:    "1234",
					Ratings:     map[int32]bool{int32(5): true},
					Import: &importJob{
						Name:  "weekly exploration",
						LbzId: EMPTY_UUID,
					},
				}

				dispatchPayload, marshalErr := json.Marshal(dispatched)
				Expect(marshalErr).To(BeNil())
				host.TaskMock.On("Enqueue", "job-queue", dispatchPayload).Return("", nil)

				err := job.Dispatch()
				Expect(err).ToNot(BeNil())
				Expect(err.Retryable).To(BeFalse())
				Expect(err.Error.Error()).To(Equal("no playlist for ListenBrainz user `test` found with algorithm/source patch `daily-jams`"))
				Expect(host.TaskMock.Calls).To(HaveLen(1))
			})
		})

		Describe("dispatchImport", func() {
			// Note, I will not be testing the "updatePlaylist" subsonic call here
			// I am assuming it just works in general (or fails).
			const URL = lbzEndpoint + "/playlist/" + EMPTY_UUID
			var (
				SINGLE_ARTIST    = types.SongRef{Name: "world.execute(me);", MBID: "9980309d-3480-4e7e-89ce-fce971a452be", Artists: []types.ArtistRef{{Name: "Mili", MBID: "d2a92ee2-27ce-4e71-bfc5-12e34fe8ef56"}}, Album: "Miracle Milk", DurationMs: 211912}
				MULTIPLE_ARTISTS = types.SongRef{
					Name:       "イザナ平原/夜",
					MBID:       "7e4bb014-51d5-4943-adb1-683e066a5220",
					Album:      "ゼノブレイド3 オリジナル・サウンドトラック",
					DurationMs: 249946,
					Artists: []types.ArtistRef{
						{Name: "ACE", MBID: "16563fb9-c2b5-4ab7-b5b1-7b6592f862a1"},
						{Name: "工藤ともり", MBID: "59e83bb6-e8b7-44e0-bea2-2275400850e5"},
						{Name: "CHiCO", MBID: "a2b0affd-963f-46a3-9a8d-5b9d1332ccb3"},
					},
				}
				DEFAULT_SONG_MATCH  = []types.SongRef{SINGLE_ARTIST}
				MULTIPLE_SONG_MATCH = []types.SongRef{SINGLE_ARTIST, MULTIPLE_ARTISTS}

				MATCH_SINGLE   = &types.Track{ID: "1234", Title: "world.execute(me);", Artist: "Mili", LibraryID: 1, Rating: 1}
				MATCH_MULTIPLE = &types.Track{ID: "5689", Title: "yzana plain / night", Artist: "ACE, TOMOri Kudo, CHiCO", LibraryID: 1}

				MATCHES  = []*types.Track{MATCH_SINGLE, MATCH_MULTIPLE}
				CREATORS = []string{"Mili", "ACE(工藤ともり、CHiCO)"}
			)

			BeforeEach(func() {
				job.JobType = ImportPlaylist
			})

			It("should error if import job is missing", func() {
				err := job.Dispatch()
				Expect(err).To(Equal(retry.FatalError("attempting to call import job without import payload")))
			})

			It("should error if lbz playlist fetch fails", func() {
				job.Import = &importJob{Name: "a playlist", LbzId: EMPTY_UUID}

				request := testdata.MakeLbzRequest(URL, "", nil)
				setupResponse(request, 404, "getPlaylist.error", nil, false)

				err := job.Dispatch()
				Expect(err).To(Equal(retry.FatalError("ListenBrainz HTTP Error. Code: 400, Error: Provided playlist ID is invalid.")))
			})

			DescribeTable("should retry on recoverable error", func(recoverable error) {
				job.Import = &importJob{Name: "a playlist", LbzId: EMPTY_UUID}
				request := testdata.MakeLbzRequest(URL, "", nil)
				setupResponse(request, 0, "", recoverable, false)
				err := job.Dispatch()
				Expect(err).To(Equal(retry.TempError(recoverable)))
			},
				Entry("connection reset", CONNECTION_RESET),
				Entry("timeout", CONTEXT_DEADLINE),
			)

			It("should error if matcher fails", func() {
				job.Import = &importJob{Name: "a playlist", LbzId: EMPTY_UUID}

				request := testdata.MakeLbzRequest(URL, "", nil)
				setupResponse(request, 200, "getPlaylist.success", nil, false)

				host.MatcherMock.On("MatchSongs", DEFAULT_SONG_MATCH, host.MatchOptions{Username: "username"}).Return([]*types.Track{}, errors.New("bad error"))

				err := job.Dispatch()
				Expect(err).To(Equal(retry.FatalError("bad error")))
				Expect(host.SubsonicAPIMock.Calls).To(BeEmpty())
			})

			DescribeTable("successful API calls", func(tracks []bool, rule map[int32]bool, expected ...string) {
				job.Import = &importJob{Name: "a playlist", LbzId: EMPTY_UUID}

				job.Ratings = map[int32]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true}
				maps.Copy(job.Ratings, rule)

				request := testdata.MakeLbzRequest(URL, "", nil)
				setupResponse(request, 200, "getPlaylist.twoTracks", nil, false)

				requestedTracks := make([]*types.Track, 2)
				for idx, track := range tracks {
					if track {
						requestedTracks[idx] = MATCHES[idx]
					}
				}

				host.MatcherMock.On("MatchSongs", MULTIPLE_SONG_MATCH, host.MatchOptions{Username: "username"}).Return(requestedTracks, nil)

				if len(expected) > 0 {
					comment := "Imported from playlist https://listenbrainz.org/playlist/00000000-0000-0000-0000-000000000000\nUpdated on: 0001-01-01T00:00:00Z"

					missing := []string{}
					for idx, track := range tracks {
						if !track {
							missing = append(missing, fmt.Sprintf("%s by %s", MULTIPLE_SONG_MATCH[idx].Name, CREATORS[idx]))
						}
					}

					if len(missing) > 0 {
						comment += "\nTracks not matched " + strings.Join(missing, ", ")
					}

					excluded := []string{}
					for _, track := range requestedTracks {
						if track != nil && !job.Ratings[track.Rating] {
							excluded = append(missing, fmt.Sprintf("%s by %s", track.Title, track.Artist))
						}
					}

					if len(excluded) > 0 {
						comment += "\nTracks excluded by rating rule: " + strings.Join(excluded, ", ")
					}

					update := url.Values{}
					update.Set("comment", comment)
					update.Set("playlistId", "C8hOrsjiVnnHZTXqxLs57t")

					testdata.MockSubsonicResponse("username", "updatePlaylist", &update, "ping.success")

					value := url.Values{}
					value.Set("username", "username")
					testdata.MockSubsonicResponse("username", "getPlaylists", &value, "noPlaylists")

					create := url.Values{}
					create.Set("name", "a playlist")
					for _, item := range expected {
						create.Add("songId", item)
					}
					testdata.MockSubsonicResponse("username", "createPlaylist", &create, "createPlaylist")
				}

				err := job.Dispatch()
				Expect(err).To(BeNil())

				if len(expected) == 0 {
					Expect(host.SubsonicAPIMock.Calls).To(BeEmpty())
				} else {
					Expect(host.SubsonicAPIMock.Calls).To(HaveLen(3))
				}
			},
				Entry("success, no matches found", []bool{false, false}, nil),
				Entry("success, no rating exists, one match", []bool{true, false}, nil, MATCH_SINGLE.ID),
				Entry("rating is excluded, two matches", []bool{true, true}, map[int32]bool{0: false}, MATCH_SINGLE.ID),
				Entry("rating is not excluded, but rating map exists", []bool{true, true}, map[int32]bool{3: false, 2: false}, MATCH_SINGLE.ID, MATCH_MULTIPLE.ID),
				Entry("all ratings are excluded, both matches", []bool{true, true}, map[int32]bool{0: false, 1: false}),
			)
		})
	})
})
