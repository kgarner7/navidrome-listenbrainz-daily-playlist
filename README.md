# Navidrome ListenBrainz Daily Playlist Importer

This repository contains a plugin for fetching daily playlists from [ListenBrainz](https://listenbrainz.org/)

## Requirements
1. Your library should have MBIDs for your tracks (or at least, most of them). This plugin does song lookups _only_ using MBID. For better fallback, having artist MBIDs will also help.
2. A ListenBrainz account per user you wish to fetch
3. If you want daily playlists (`daily-jams`), you should follow the [`troi-bot`](https://listenbrainz.org/user/troi-bot/) user
4. Navidrome >= 0.60.0. This reworked the plugin API. When upgrading to this version, you will need to install a new plugin. For older versions of Navidrome, see https://github.com/kgarner7/navidrome-listenbrainz-daily-playlist/tree/v2.0.4

## Install instructions

### From GitHub Release

You can download the `listenbrainz-daily-playlist.ndp` from the latest release and then run `navidrome plugin install listenbrainz-daily-playlist.ndp`.
Make sure to run this command as your navidrome user.
This will unzip the package, and install it automatically in your plugin directory.

### From source

Requirements:
- `go` 1.25
- [`tinygo`](https://tinygo.org/) (recommended)

#### Build WASM plugin

##### Using stock golang

```bash
make
```

This is a development build of the plugin. Compilation should be _extremely_ fast

##### Using TinyGo
```bash
make prod
```

### Install

Put the the `listenbrainz-daily-playlist.ndp` file in your Navidrome `Plugins.Folder`.

Make sure that:
1. You have plugins enabled (`Plugins.Enabled = true`, `ND_PLUGINS_ENABLED = true`).
2. Your Navidrome user has read permissions in the plugin directory

As an admin user open the plugin page (profile icon > plugins) and enable the `listenbrainz-metadata-provider` plugin.
Note that you will need to configure it before use.

### Configuration

To make the plugin useful, you will need to configure it.
This requires configuring one or more users, and specifying which playlists to use.

- `Navidrome username`: this is the username of the Navidrome account you want to enable. This user must also be selected in the `User Permission` block
- `ListenBrainz username`: the user's ListenBrainz username
- `Source`: This is a ListenBrainz internal field which specifies how the playlist is generated. Examples include `weekly-jams`, `daily-jams` and `weekly-exploration`.
- `Playlist name to be imported`: the name of the playlist that will be created within Navidrome. **CAUTION**: if a playlist with this name already exists, it will be overridden.
- `Include tracks with this rating.`: if you only want to import tracks with certain ratings, uncheck one or more boxes
- `Schedule to fetch playlists`: how often to fetch playlists
- `Fallback search count`: If a match isn't found by track name, how many tracks to search by until giving up. Between 1 and 500, inclusive
- `Check for out of date playlists on plugin start`: If Navidrome or the plugin is restarted, check if any playlists are out of date (at least three hours old).

To get another valid `source`, visit your `https://listenbrainz.org/user/<your username>/recommendations/`.
Click on the inspect playlist `</>` icon and use the string in `source_patch`.

![Image depicting how to find the source patch. Surrounded by a red square (overlayed) in the foreground is the `source-patch` string. In the background, also in a red square is the button that was used to open the inspect listen modal](./assets/source_patch.png)
