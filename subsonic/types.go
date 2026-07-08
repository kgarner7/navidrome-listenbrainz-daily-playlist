package subsonic

import "time"

// This is not meant to be a complete implementation of the Subsonic types.
// It is only meant to provide the bare minimum necessary for the plugin to operate

type Error struct {
	Code    int32  `xml:"code,attr"                      json:"code"`
	Message string `xml:"message,attr"                   json:"message"`
}

type Playlist struct {
	Id      string    `xml:"id,attr"                       json:"id"`
	Name    string    `xml:"name,attr"                     json:"name"`
	Comment string    `xml:"comment,attr,omitempty"        json:"comment,omitempty"`
	Public  bool      `xml:"public,attr"                   json:"public,omitempty"`
	Changed time.Time `xml:"changed,attr"                  json:"changed"`
}

type Playlists struct {
	Playlist []Playlist `xml:"playlist"                           json:"playlist,omitempty"`
}

type Subsonic struct {
	Status    string     `xml:"status,attr"                                   json:"status"`
	Error     *Error     `xml:"error,omitempty"                               json:"error,omitempty"`
	Playlists *Playlists `xml:"playlists,omitempty"                           json:"playlists,omitempty"`
	Playlist  *Playlist  `xml:"playlist,omitempty"                            json:"playlist,omitempty"`
}

type JsonWrapper struct {
	Subsonic Subsonic `json:"subsonic-response"`
}
