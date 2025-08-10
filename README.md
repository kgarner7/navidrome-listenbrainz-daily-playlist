# Navidrome ListenBrainz Daily Playlist Importer

This repository contains a plugin for fetching daily playlists from [ListenBrainz](https://listenbrainz.org/)

## Requirements
1. Your library should have MBIDs for your tracks (or at least, most of them). This plugin does song lookups _only_ using MBID. For better fallback, having artist MBIDs will also help.
2. A ListenBrainz account per user you wish to fetch
3. If you want daily playlists (`daily-jams`), you should follow the [`troi-bot`](https://listenbrainz.org/user/troi-bot/) user
4. Navidrome >= 0.58.0. 0.58.0 introduces the ability to get current time, which is required for this version.

## Install from source

Requirements:
- `go` 1.24

### Build WASM plugin

```bash
go mod download
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm plugin.go
```

### Package plugin

Copy the following files: `manifest.json` and `plugin.wasm`. 
Put them in a directory in your Navidrome `Plugins.Folder`.
Make sure that:
1. You have plugins enabled (`Plugins.Enabled = true`, `ND_PLUGINS_ENABLED = true`).
2. Your Navidrome user has read permissions in the plugin directory

Restart Navidrome and you should see the following message in your logs (`info` or more verbose)

```
level=info msg="Discovered plugin" capabilities="[LifecycleManagement SchedulerCallback]" dev_mode=false folder=listenbrainz-daily-playlist name=listenbrainz-daily-playlist wasm=PATH_TO_WASM_PLUGIN
```

## Configuration

```toml
# A complete configuration

[Plugins]
Enabled = true
# Optional, if you want to specify a different path. Otherwise it is your data directory / plugins
Folder = "SOME_CUSTOM_PATH_TO_PLUGINS"

[PluginConfig.listenbrainz-daily-playlist]
Split = ";"
Schedule = "@every 24h"

Sources = "daily-jams;weekly-jams"
"Sources[0]" = "ListenBrainz Daily Jams"
"Sources[1]" = "ListenBrainz Weekly Jams"

Users = "user1;user2"
"Users[0]" = "lb-uzername-1"
"Users[1]" = "lb-uzername-2"
```

The configuration has four main blocks:

### Split
This is an optional section, but it defines how multi-valued fields are split.
The default value if not specified is `;`

### Schedule
This is a schedule instructing how often to run the sync.
The sample configuration `@every 24h` does it every 24 hours.
This is the default option, so it can be omitted.
Please note that ListenBrainz playlists are only updated once a day.

Also note, when Navidrome is restarted, this plugin will check if any playlists are out of date, and if so, fetch them.

### Sources
This specifies the source(s) to fetch from ListenBrainz, and what name to use when importing into Navidrome.
Source names include:

- `daily-jams`: daily playlist of tracks you've listened to before
- `weekly-jams`: weekly playlist of tracks you've listened to before
- `weekly-exploration`: weekly playlist of new tracks. This is likely to have few matches in your library

`Sources` itself is multi-valued, split by the `Split` token.
So, in the example above, you have two sources: `daily-jams` and `weekly-jams`.

For each source, you must then specify the name to use when importing.
The key is `sources[zero-based-index]`, and the value is the playlist name.

In the above example, this means:
1. Import `daily-jams` playlists with the name `ListenBrainz Daily Jams`
2. Import `weekly-jams` playlists with the name `ListenBrainz Weekly Jams`

### Users
This specifies which Subsonic/Navidrome users to fetch, and provides their ListenBrainz username.

In the example provided, there are two users, `user1` and `user`. 
Their ListenBrainz usernames are `lb-uzername-1` and `lb-uzername-2`, respectively.

### Optional settings

#### Disable cehcking on startup
To disable fetching on service start, pass in the following config to the plugin:

```toml
CheckOnStartup = "false"
```

Note that this value needs to be in quotes (not a boolean).


#### Only include songs with a specific rating
To only import songs with a given rating, you can pass in `rating[idx]` as a comma-separated value of ratings.
Note that `0` means no rating

```toml
Users = "user1;user2"
"Users[0]" = "lb-uzername-1"

# Exclude songs with a 1-star rating
"Rating[0]" = "0,2,3,4,5"
```

#### Specify number of tracks to fetch with fallback
```toml
FallbackCount = "15"
```

When searching by title when no specific match is found, this limits the number of potential track matches to search by name.
By default, this value is 15, but may be decreased to 1 or increased up to 500.


## How does it work?
This plugin relies on a special quick of Navidrome, wherein using the `/rest/search3` endpoint by MBID will return exact matches.
So, for most matches, you will want to make sure all your files have MBIDs.

In the event that there is no match by track MBID (for example, ListenBrainz returns a _different_ track ID than the one you have), then the lookup process is as follows:
1. Map every artist MBID in the track provided by ListenBrainz to a Subsonic artist ID. This is similarly done through `/rest/search3`, but for artists instead of tracks.
2. Search for tracks with the name given by ListenBrainz.
3. Find a track whose name is an **exact match** of the ListenBrainz track, and whose artists **all** match the artist IDS found in step 1.

This fallback is of course not perfect.
If your track name doesn't match **exactly** what ListenBrainz provides, or one or more of the artists doesn't have an MBID, then it will not be found.


## Permissions
- `config`: This plugin needs to read the config to determine what users to fetch, and so on
- `http`: make requests to ListenBrainz API to fetch playlists
- `scheduler`: schedule periodic task
- `subsonicapi`, all users and admin: while this plugin requests access to all users, it only needs permissions for users specified in the configuration. It uses the following endpoints: `search3`, `getPlaylists`, `createPlaylist`, and `updatePlaylist`