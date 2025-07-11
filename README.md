# Navidrome ListenBrainz Daily Playlist Importer

This repository contains a plugin for fetching daily playlists from [ListenBrainz](https://listenbrainz.org/)

## Configuration

```toml
# A complete configuration
[PluginConfig.listenbrainz-daily-playlist]
Split = ";"

Schedule = "0 3 * * *"

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
This is a cron schedule instructing how often to run the sync.
Since ListenBrainz playlists are only updated once a day, it is recommended to only do it once a day.
The sample configuration `0 3 * * *` does it every day at 3:00 AM 

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

## How does it work?
This plugin relies on a special quick of Navidrome, wherein using the `/rest/search3` endpoint by MBID will return exact matches.
So, for most matches, you will want to make sure all your files have MBIDs.

## Permissions
- `config`: This plugin needs to read the config to determine what users to fetch, and so on
- `http`: make requests to ListenBrainz API to fetch playlists
- `scheduler`: schedule periodic task
- `subsonicapi`, all users and admin: while this plugin requests access to all users, it only needs permissions for users specified in the configuration. It uses the following endpoints: `search3`, `getPlaylists`, `createPlaylist`, and `updatePlaylist`