package dispatcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"listenbrainz-daily-playlist/listenbrainz"
	"listenbrainz-daily-playlist/retry"
	"listenbrainz-daily-playlist/subsonic"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/navidrome/navidrome/plugins/pdk/go/host"
	"github.com/navidrome/navidrome/plugins/pdk/go/pdk"
	"github.com/navidrome/navidrome/plugins/pdk/go/types"
)

const (
	taskTimeMs = 30_000
	queueName  = "job-queue"
)

// Dispatches a job.
// The first return value denotes an unrecoverable error (do not retry)
// The second return value is an error that should be reattempted
func (j *Job) Dispatch() *retry.Error {
	switch j.JobType {
	case FetchPatches:
		return j.dispatchSourceFetching()
	case GenerateJams:
		return j.dispatchGenerate()
	case ImportPlaylist:
		return j.dispatchImport()
	default:
		return retry.FatalError(fmt.Sprintf("unexpected job %s", j.JobType))
	}
}

func (j *Job) dispatchSourceFetching() *retry.Error {
	if j.Patch == nil {
		return retry.FatalError("attempting to dispatch patch fetch without patch")
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Searching ListenBrainz for generated playlists for %s", j.Username))

	playlists, err := listenbrainz.GetCreatedForPlaylists(j.LbzUsername, j.LbzToken)
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Failed to fetch playlists for user %s: %v", j.Username, err.Error))
		return err
	}

	var ignoredError error = nil

	for _, source := range j.Patch.Sources {
		playlistId := ""

		for _, plsMetadata := range playlists {
			if plsMetadata.Extension.Extension.AdditionalMetadata.AlgorithmMetadata.SourcePatch == source.SourcePatch {
				playlistId = listenbrainz.GetIdentifier(plsMetadata.Identifier)
				break
			}
		}

		if playlistId == "" {
			newErr := fmt.Errorf("no playlist for ListenBrainz user `%s` found with algorithm/source patch `%s`", j.LbzUsername, source.SourcePatch)
			ignoredError = errors.Join(ignoredError, newErr)
			pdk.Log(pdk.LogError, newErr.Error())
			continue
		}

		newJob := Job{
			JobType:     ImportPlaylist,
			Username:    j.Username,
			LbzUsername: j.LbzUsername,
			LbzToken:    j.LbzToken,
			Ratings:     j.Ratings,
			Import: &importJob{
				Name:  source.PlaylistName,
				LbzId: playlistId,
			},
		}

		payload, serializeErr := json.Marshal(newJob)
		if serializeErr != nil {
			pdk.Log(pdk.LogError, fmt.Sprintf("Error serializing import job: %v", serializeErr))
			ignoredError = errors.Join(ignoredError, serializeErr)
			continue
		}

		_, taskErr := host.TaskEnqueue(queueName, payload)
		if taskErr != nil {
			return retry.TempError(taskErr)
		}
	}

	if ignoredError != nil {
		return &retry.Error{Error: ignoredError, Retryable: false}
	}

	return nil
}

func (j *Job) dispatchGenerate() *retry.Error {
	if j.Generate == nil {
		return retry.FatalError("attempting to call generate job without generate payload")
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Generating playlist `%s` for user %s", j.Generate.Name, j.Username))

	now := time.Now()

	recommendations, err := listenbrainz.GetRecommendations(j.LbzUsername, j.LbzToken)
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Unable to fetch recommendations for user %s: %v", j.Username, err.Error))
		return err
	}

	mbids := make([]string, len(recommendations.Payload.MBIDs))
	for idx, recording := range recommendations.Payload.MBIDs {
		mbids[idx] = recording.RecordingMBID
	}

	metadata, err := listenbrainz.LookupRecordings(mbids, j.LbzToken)
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Unable to lookup %d recordings for user %s: %v", len(mbids), j.Username, err.Error))
		return err
	}

	allowedSongs := []*types.Track{}
	notPlayed := []*types.Track{}

	tracks := make([]types.SongRef, len(mbids))

	for idx, mbid := range mbids {
		recordingMetadata, ok := metadata[mbid]
		if !ok {
			pdk.Log(pdk.LogWarn, fmt.Sprintf("Warning: track with mbid %s not found in metadata lookup. Skipping", mbid))
			continue
		}

		tracks[idx].Album = recordingMetadata.Release.Name
		tracks[idx].AlbumMBID = recordingMetadata.Release.MBID
		tracks[idx].DurationMs = recordingMetadata.Recording.Length
		tracks[idx].Name = recordingMetadata.Recording.Name
		tracks[idx].MBID = mbid

		if len(recordingMetadata.Recording.ISRCs) > 0 {
			tracks[idx].ISRC = recordingMetadata.Recording.ISRCs[0]
		}

		tracks[idx].Artists = make([]types.ArtistRef, len(recordingMetadata.Artist.Artists))

		for artistIdx, artist := range recordingMetadata.Artist.Artists {
			tracks[idx].Artists[artistIdx].Name = artist.Name
			tracks[idx].Artists[artistIdx].MBID = artist.ArtistMbid
		}
	}

	matches, matchErr := host.MatcherMatchSongs(tracks, host.MatchOptions{Username: j.Username})

	if matchErr != nil {
		return &retry.Error{Error: matchErr, Retryable: false}
	}

	missing := []string{}
	excluded := []string{}
	recentCount := 0

	for idx, song := range matches {
		if song != nil {
			if !j.Ratings[song.Rating] {
				excluded = append(excluded, song.Title)
				continue
			}

			if song.PlayDate == nil {
				notPlayed = append(notPlayed, song)
				continue
			}

			playTime := time.Unix(*song.PlayDate, 0)

			if now.Sub(playTime).Hours() < float64(j.Generate.TrackAge*24) {
				recentCount += 1
				pdk.Log(pdk.LogTrace, fmt.Sprintf("Excluding track `%s` for being played recently", song.Title))
				continue
			}

			allowedSongs = append(allowedSongs, song)
		} else {
			missing = append(missing, tracks[idx].Name)
		}
	}

	if len(allowedSongs) < 50 {
		unlistenedCount := min(50-len(allowedSongs), len(notPlayed))
		allowedSongs = append(allowedSongs, notPlayed[0:unlistenedCount]...)
	}

	songIds := []string{}

	if j.Generate.ArtistLimit == 0 {
		for _, song := range allowedSongs[:min(len(allowedSongs), 50)] {
			songIds = append(songIds, song.ID)
		}
	} else {
		artistCredits := map[string]int{}

	outer:
		for _, song := range allowedSongs {
			for _, artist := range song.Participants {
				if artist.Role == "artist" {
					count := artistCredits[artist.ID]
					if count >= j.Generate.ArtistLimit {
						continue outer
					}
				}
			}

			songIds = append(songIds, song.ID)
			if len(songIds) == 50 {
				break outer
			}

			for _, artist := range song.Participants {
				if artist.Role == "artist" {
					artistCredits[artist.ID] += 1
				}
			}
		}
	}

	recsUpdated := time.Unix(recommendations.Payload.LastUpdated, recommendations.Payload.LastUpdated).Format(time.RFC1123)

	comment := fmt.Sprintf(
		"Jams generated on %s with %d recommendations generated on %s."+
			"\nExcluded by rating rules: %s\nTracks not found in library: %s\nExcluded for being recent: %d",
		now.Format(time.RFC1123), len(mbids), recsUpdated,
		strings.Join(excluded, ", "),
		strings.Join(missing, ", "),
		recentCount,
	)

	err = subsonic.UpdatePlaylist(j.Username, j.Generate.Name, comment, songIds)
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Unable to import playlist `%s` for user %s: %v", j.Generate.Name, j.Username, err.Error))
		return err
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Successfully generated playlist `%s` for user %s", j.Generate.Name, j.Username))
	return nil
}

func (j *Job) dispatchImport() *retry.Error {
	if j.Import == nil {
		return retry.FatalError("attempting to call import job without import payload")
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Importing playlist `%s` (%s)", j.Import.Name, j.Import.LbzId))

	playlist, err := listenbrainz.GetPlaylist(j.Import.LbzId, j.LbzToken)
	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Unable to import playlist %s: %v", j.Import.LbzId, err.Error))
		return err
	}

	tracks := make([]types.SongRef, len(playlist.Tracks))

	for idx, track := range playlist.Tracks {
		mbid := listenbrainz.GetIdentifier(track.Identifier[0])
		tracks[idx].Album = track.Album
		tracks[idx].DurationMs = track.Duration
		tracks[idx].Name = track.Title
		tracks[idx].MBID = mbid

		tracks[idx].Artists = make([]types.ArtistRef, len(track.Extension.Track.AdditionalMetadata.Artists))

		for artistIdx, artist := range track.Extension.Track.AdditionalMetadata.Artists {
			tracks[idx].Artists[artistIdx].Name = artist.ArtistCreditName
			tracks[idx].Artists[artistIdx].MBID = artist.MBID
		}
	}

	matches, matchErr := host.MatcherMatchSongs(tracks, host.MatchOptions{Username: j.Username})

	if matchErr != nil {
		return &retry.Error{Error: matchErr, Retryable: false}
	}

	songIds := []string{}
	missing := []string{}
	excluded := []string{}

	for idx, song := range matches {
		if song != nil {
			if j.Ratings[song.Rating] {
				songIds = append(songIds, song.ID)
			} else {
				excluded = append(excluded, fmt.Sprintf("%s by %s", song.Title, song.Artist))
			}
		} else {
			missing = append(missing, fmt.Sprintf("%s by %s", tracks[idx].Name, playlist.Tracks[idx].Creator))
		}
	}

	name := j.Import.Name

	if len(songIds) == 0 {
		pdk.Log(pdk.LogWarn, fmt.Sprintf("No matching files found for playlist %s. Refusing to create/update", name))
		return nil
	}

	comment := fmt.Sprintf("Imported from playlist %s\nUpdated on: %s", playlist.Identifier, playlist.Date)

	if len(missing) > 0 {
		comment += fmt.Sprintf("\nTracks not matched by track MBID or track name + artist MBIDs: %s", strings.Join(missing, ", "))
	}

	if len(excluded) > 0 {
		comment += fmt.Sprintf("\nTracks excluded by rating rule: %s", strings.Join(excluded, ", "))
	}

	err = subsonic.UpdatePlaylist(j.Username, name, comment, songIds)

	if err != nil {
		pdk.Log(pdk.LogError, fmt.Sprintf("Failed to import playlist `%s` for user %s: %v", name, j.Username, err.Error))
		return err
	}

	pdk.Log(pdk.LogInfo, fmt.Sprintf("Successfully processed playlist `%s` for user %s", name, j.Username))
	return nil
}

func GetConfig() ([]userConfig, error) {
	users, ok := pdk.GetConfig("users")
	if !ok {
		return nil, errors.New("missing required 'users' configuration")
	}

	userMapping := []userConfig{}
	err := json.Unmarshal([]byte(users), &userMapping)
	if err != nil {
		return nil, fmt.Errorf("invalid user mapping: %s. Should be a mapping of Navidrome users to ListenBrainz usernames", users)
	}

	for _, user := range userMapping {
		names := map[string]bool{}

		if user.NDUsername == "" || user.LbzUsername == "" {
			return nil, errors.New("user must have a Navidrome username and ListenBrainz username")
		}

		if len(user.Sources) > 0 {
			for _, source := range user.Sources {
				_, existing := names[source.PlaylistName]
				if existing {
					return nil, fmt.Errorf("duplicate playlist name found: %s", source.PlaylistName)
				}

				names[source.PlaylistName] = true
			}
		}

		if user.GeneratePlaylist && user.GeneratedPlaylist != "" {
			_, existing := names[user.GeneratedPlaylist]
			if existing {
				return nil, fmt.Errorf("duplicate playlist name found: %s", user.GeneratedPlaylist)
			}

			names[user.GeneratedPlaylist] = true
		}

		if len(user.Playlists) > 0 {
			for _, playlist := range user.Playlists {
				_, existing := names[playlist.Name]
				if existing {
					return nil, fmt.Errorf("duplicate playlist name found: %s", playlist.Name)
				}
				names[playlist.Name] = true
			}
		}
	}

	return userMapping, nil
}

func parseRatings(ratingString []string) map[int32]bool {
	ratings := map[int32]bool{}

	for _, rating := range ratingString {
		ratingInt, err := strconv.ParseInt(rating, 10, 32)
		if err != nil {
			continue
		}

		if ratingInt >= 0 && ratingInt <= 5 {
			ratings[int32(ratingInt)] = true
		}
	}

	if len(ratings) == 0 {
		ratings = map[int32]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true}
	}

	return ratings
}

func InitialFetch() error {
	users, err := GetConfig()
	if err != nil {
		return err
	}

	nowTs := time.Now()

	missing := []string{}
	olderThanThreeHours := []string{}

	jobs := []Job{}

	for _, user := range users {
		playlistResp, err := subsonic.Call("getPlaylists", user.NDUsername, &url.Values{"username": []string{user.NDUsername}})
		if err != nil {
			return errors.New("failed to fetch playlists on initial fetch")
		}

		fetchedSources := []source{}
		rating := parseRatings(user.Ratings)

		if len(user.Sources) > 0 {
			for _, source := range user.Sources {
				pls := subsonic.FindExistingPlaylist(playlistResp, source.PlaylistName)

				if pls == nil {
					missing = append(missing, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, source.PlaylistName))
					fetchedSources = append(fetchedSources, source)
					continue
				}

				if nowTs.Sub(pls.Changed) > 3*time.Hour {
					olderThanThreeHours = append(olderThanThreeHours, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, source.PlaylistName))
					fetchedSources = append(fetchedSources, source)
				}
			}
		}

		if len(fetchedSources) > 0 {
			jobs = append(jobs, Job{
				JobType:     FetchPatches,
				Username:    user.NDUsername,
				LbzUsername: user.LbzUsername,
				LbzToken:    user.LbzToken,
				Ratings:     rating,
				Patch: &patchJob{
					Sources: fetchedSources,
				},
			})
		}

		if user.GeneratePlaylist && user.GeneratedPlaylist != "" {
			pls := subsonic.FindExistingPlaylist(playlistResp, user.GeneratedPlaylist)
			shouldGenerate := false

			if pls == nil {
				missing = append(missing, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, user.GeneratedPlaylist))
				shouldGenerate = true
			} else if nowTs.Sub(pls.Changed) > 3*time.Hour {
				olderThanThreeHours = append(olderThanThreeHours, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, user.GeneratedPlaylist))
				shouldGenerate = true
			}

			if shouldGenerate {
				jobs = append(jobs, Job{
					JobType:     GenerateJams,
					Username:    user.NDUsername,
					LbzUsername: user.LbzUsername,
					LbzToken:    user.LbzToken,
					Ratings:     rating,
					Generate: &generationJob{
						Name:        user.GeneratedPlaylist,
						ArtistLimit: user.GeneratedPlaylistArtistLimit,
						TrackAge:    user.GeneratedPlaylistTrackAge,
					},
				})
			}
		}

		if len(user.Playlists) > 0 {
			for _, item := range user.Playlists {
				pls := subsonic.FindExistingPlaylist(playlistResp, item.Name)
				shouldImport := false

				if pls == nil {
					missing = append(missing, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, item.Name))
					shouldImport = true
				} else if !item.OneTime && nowTs.Sub(pls.Changed) > 3*time.Hour {
					olderThanThreeHours = append(olderThanThreeHours, fmt.Sprintf("User: `%s`, Source: `%s`", user.NDUsername, item.Name))
					shouldImport = true
				}

				if shouldImport {
					jobs = append(jobs, Job{
						JobType:     ImportPlaylist,
						Username:    user.NDUsername,
						LbzUsername: user.LbzUsername,
						LbzToken:    user.LbzToken,
						Ratings:     rating,
						Import: &importJob{
							Name:  item.Name,
							LbzId: item.LbzId,
						},
					})
				}
			}
		}
	}

	if len(jobs) > 0 {
		pdk.Log(pdk.LogInfo,
			fmt.Sprintf("Missing or outdated playlists, fetching on initial sync. Missing: %v, Outdated: %v",
				missing,
				olderThanThreeHours,
			))

		for _, job := range jobs {
			payload, err := json.Marshal(job)
			if err != nil {
				return err
			}

			_, err = host.TaskEnqueue(queueName, payload)
			if err != nil {
				return err
			}
		}
	} else {
		pdk.Log(pdk.LogInfo, "No missing/outdated playlists, not fetching")
	}

	return nil
}

func CreateQueue() error {
	return host.TaskCreateQueue(queueName, host.QueueConfig{
		Concurrency: 1,
		MaxRetries:  5,
		RetentionMs: 60_000,
		BackoffMs:   5 * taskTimeMs,
		DelayMs:     taskTimeMs,
	})
}

func ClearQueue() {
	count, err := host.TaskClearQueue(queueName)
	if err != nil {
		pdk.Log(pdk.LogError, "Failed to clear task queue: "+err.Error())
	} else if count > 0 {
		pdk.Log(pdk.LogInfo, fmt.Sprintf("Removed %d job(s) from task queue", count))
	}
}
