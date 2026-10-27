package youtubemusic

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

func parseAccount(payload any) Account {
	var account Account
	walkObjects(payload, func(object map[string]any) {
		if account.Name != "" {
			return
		}
		renderer := objectMap(object, "activeAccountHeaderRenderer")
		if renderer == nil && object["accountName"] != nil {
			renderer = object
		}
		if renderer == nil {
			return
		}
		account.Name = textAt(renderer, "accountName")
	})
	return account
}

func parsePlaylists(payload any) []Playlist {
	var playlists []Playlist
	walkObjects(payload, func(object map[string]any) {
		renderer := objectMap(object, "musicTwoRowItemRenderer")
		if renderer == nil {
			return
		}
		browseID := browseID(renderer)
		if !strings.HasPrefix(browseID, "VL") {
			return
		}
		playlistID := strings.TrimPrefix(browseID, "VL")
		if playlistID == "" {
			return
		}
		playlist := Playlist{ID: playlistID, Title: textAt(renderer, "title")}
		playlist.TrackCount = countFromText(textAt(renderer, "subtitle"))
		playlists = append(playlists, playlist)
	})
	return playlists
}

func uniquePlaylists(input []Playlist) []Playlist {
	seen := make(map[string]bool)
	output := make([]Playlist, 0, len(input))
	for _, playlist := range input {
		if playlist.ID == "" || seen[playlist.ID] {
			continue
		}
		seen[playlist.ID] = true
		output = append(output, playlist)
	}
	return output
}

func parsePlaylist(payload any) Playlist {
	var playlist Playlist
	walkObjects(payload, func(object map[string]any) {
		if playlist.Title != "" {
			return
		}
		for _, key := range []string{"musicEditablePlaylistDetailHeaderRenderer", "musicDetailHeaderRenderer", "musicResponsiveHeaderRenderer"} {
			renderer := objectMap(object, key)
			if renderer == nil {
				continue
			}
			playlist.Title = textAt(renderer, "title")
			playlist.ID = strings.TrimPrefix(browseID(renderer), "VL")
			// The subtitle holds the year; the second one starts with the song count.
			playlist.TrackCount = countFromText(textAt(renderer, "secondSubtitle"))
			return
		}
	})
	return playlist
}

func parseTracks(payload any) []Track {
	var shelves []any
	walkObjects(payload, func(object map[string]any) {
		if renderer := objectMap(object, "musicPlaylistShelfRenderer"); renderer != nil {
			shelves = append(shelves, renderer["contents"])
		}
	})
	if len(shelves) == 0 {
		return parseTrackRenderers(payload)
	}
	var tracks []Track
	for _, shelf := range shelves {
		tracks = append(tracks, parseTrackRenderers(shelf)...)
	}
	return tracks
}

func parseTrackRenderers(payload any) []Track {
	var tracks []Track
	walkObjects(payload, func(object map[string]any) {
		renderer := objectMap(object, "musicResponsiveListItemRenderer")
		if renderer == nil {
			return
		}
		videoID := stringAt(renderer, "playlistItemData", "videoId")
		if videoID == "" {
			videoID = findStringKey(renderer, "videoId")
		}
		columns := arrayAt(renderer, "flexColumns")
		if len(columns) == 0 {
			return
		}
		titleRuns := runsAt(columns[0], "musicResponsiveListItemFlexColumnRenderer", "text")
		title := runsText(titleRuns)
		if videoID == "" && len(titleRuns) > 0 {
			videoID = stringAt(titleRuns[0], "navigationEndpoint", "watchEndpoint", "videoId")
		}
		if title == "" {
			return
		}
		track := Track{VideoID: videoID, Title: title, Available: videoID != "", SetVideoID: stringAt(renderer, "playlistItemData", "playlistSetVideoId")}
		if thumbnails := arrayAt(renderer, "thumbnail", "musicThumbnailRenderer", "thumbnail", "thumbnails"); len(thumbnails) > 0 {
			track.Thumbnail = stringAt(thumbnails[len(thumbnails)-1], "url")
		}
		if len(columns) > 1 {
			for _, value := range runsAt(columns[1], "musicResponsiveListItemFlexColumnRenderer", "text") {
				run, _ := value.(map[string]any)
				name := strings.TrimSpace(stringValue(run["text"]))
				if name == "" || name == "•" {
					continue
				}
				// Search results show the duration here rather than in a fixed column.
				if duration, ok := parseDuration(name); ok {
					track.Duration = duration
					continue
				}
				id := stringAt(run, "navigationEndpoint", "browseEndpoint", "browseId")
				if id != "" && !strings.HasPrefix(id, "MPRE") {
					track.Artists = append(track.Artists, Artist{Name: name})
				}
			}
		}
		for _, column := range arrayAt(renderer, "fixedColumns") {
			value := textAt(column, "musicResponsiveListItemFixedColumnRenderer", "text")
			if duration, ok := parseDuration(value); ok {
				track.Duration = duration
				break
			}
		}
		tracks = append(tracks, track)
	})
	return tracks
}

func findPlaylistContinuation(payload any) string {
	return findScopedContinuation(payload,
		"musicPlaylistShelfRenderer",
		"musicPlaylistShelfContinuation",
	)
}

func findLibraryContinuation(payload any) string {
	return findScopedContinuation(payload,
		"gridRenderer",
		"musicGridRenderer",
		"gridContinuation",
	)
}

func findScopedContinuation(payload any, rendererKeys ...string) string {
	var result string
	walkObjects(payload, func(object map[string]any) {
		if result != "" {
			return
		}
		for _, key := range rendererKeys {
			renderer := objectMap(object, key)
			if renderer == nil {
				continue
			}
			// Continuations attached to a renderer belong to that renderer. Do
			// not scan its complete subtree: playlist pages also contain a
			// section-list continuation for recommendations.
			result = findAnyContinuation(renderer["continuations"])
			if result != "" {
				return
			}
			// Newer pages end the renderer's own contents with the token.
			contents, _ := renderer["contents"].([]any)
			if len(contents) > 0 {
				result = findAnyContinuation(objectMap(contents[len(contents)-1], "continuationItemRenderer"))
				if result != "" {
					return
				}
			}
		}
		if action := objectMap(object, "appendContinuationItemsAction"); action != nil {
			result = findAnyContinuation(action["continuationItems"])
		}
	})
	return result
}

func findAnyContinuation(payload any) string {
	var result string
	walkObjects(payload, func(object map[string]any) {
		if result != "" {
			return
		}
		if value := stringAt(object, "nextContinuationData", "continuation"); value != "" {
			result = value
			return
		}
		if value := stringAt(object, "continuationCommand", "token"); value != "" {
			result = value
		}
	})
	return result
}

func responseShape(payload any) string {
	keys := make(map[string]bool)
	walkObjects(payload, func(object map[string]any) {
		for key := range object {
			if strings.HasSuffix(key, "Renderer") || strings.HasSuffix(key, "Action") {
				keys[key] = true
			}
		}
	})
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Strings(result)
	if len(result) == 0 {
		return "no renderers"
	}
	return strings.Join(result, ", ")
}

func responseLoggedOut(payload any) bool {
	context := objectMap(payload, "responseContext", "mainAppWebResponseContext")
	loggedOut, _ := context["loggedOut"].(bool)
	return loggedOut
}

func walkObjects(value any, visit func(map[string]any)) {
	switch typed := value.(type) {
	case map[string]any:
		visit(typed)
		for _, child := range typed {
			walkObjects(child, visit)
		}
	case []any:
		for _, child := range typed {
			walkObjects(child, visit)
		}
	}
}

func objectMap(value any, path ...string) map[string]any {
	current, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	for _, key := range path {
		next, ok := current[key].(map[string]any)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func arrayAt(value any, path ...string) []any {
	if len(path) == 0 {
		result, _ := value.([]any)
		return result
	}
	parent := objectMap(value, path[:len(path)-1]...)
	if parent == nil {
		return nil
	}
	result, _ := parent[path[len(path)-1]].([]any)
	return result
}

func stringAt(value any, path ...string) string {
	if len(path) == 0 {
		return stringValue(value)
	}
	parent := objectMap(value, path[:len(path)-1]...)
	if parent == nil {
		return ""
	}
	return stringValue(parent[path[len(path)-1]])
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func textAt(value any, path ...string) string {
	current := value
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = object[key]
	}
	if object, ok := current.(map[string]any); ok {
		if simple := stringValue(object["simpleText"]); simple != "" {
			return simple
		}
		return runsText(arrayAt(object, "runs"))
	}
	return stringValue(current)
}

func runsAt(value any, path ...string) []any {
	object := objectMap(value, path...)
	if object == nil {
		return nil
	}
	return arrayAt(object, "runs")
}

func runsText(runs []any) string {
	var output strings.Builder
	for _, value := range runs {
		run, _ := value.(map[string]any)
		output.WriteString(stringValue(run["text"]))
	}
	return strings.TrimSpace(output.String())
}

func browseID(value any) string {
	if direct := stringAt(value, "navigationEndpoint", "browseEndpoint", "browseId"); direct != "" {
		return direct
	}
	return findStringKey(value, "browseId")
}

func hasKey(value any, key string) bool {
	found := false
	walkObjects(value, func(object map[string]any) {
		if _, ok := object[key]; ok {
			found = true
		}
	})
	return found
}

func findStringKey(value any, key string) string {
	var result string
	walkObjects(value, func(object map[string]any) {
		if result == "" {
			result = stringValue(object[key])
		}
	})
	return result
}

func countFromText(value string) int {
	for _, field := range strings.Fields(strings.ReplaceAll(value, ",", "")) {
		if count, err := strconv.Atoi(field); err == nil && count >= 0 {
			return count
		}
	}
	return 0
}

func parseDuration(value string) (time.Duration, bool) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	seconds := 0
	for index, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || (index > 0 && value >= 60) {
			return 0, false
		}
		seconds = seconds*60 + value
	}
	return time.Duration(seconds) * time.Second, true
}
