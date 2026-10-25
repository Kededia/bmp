package app

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"bmp/internal/playback"
	"bmp/internal/theme"
	"bmp/internal/youtubemusic"
)

func (m Model) musicMainView(width, height int) string {
	innerWidth := max(8, width-4)
	contentHeight := max(1, height-3) // Panel borders and shortcut footer.
	if contentHeight < 3 {
		return m.youtubeMusicBrowserView(innerWidth, contentHeight)
	}
	const gap = 2
	infoWidth := max(4, (innerWidth-gap)/2)
	spectrumWidth := max(4, innerWidth-gap-infoWidth)
	topHeight := min(8, max(1, (contentHeight-2)/3))

	info := lipgloss.NewStyle().Width(infoWidth).Height(topHeight).Render(m.youtubeMusicInfoView(infoWidth))
	visualizer := lipgloss.NewStyle().Width(spectrumWidth).Height(topHeight).Render(m.youtubeMusicSpectrumPanel(spectrumWidth, topHeight))
	top := lipgloss.JoinHorizontal(lipgloss.Top, info, strings.Repeat(" ", gap), visualizer)
	browserHeight := contentHeight - lipgloss.Height(top) - 2
	// The playing song's cover fills the space under the spectrum.
	if m.coverRender != nil {
		m.coverRender.draw = ""
	}
	// One line short of the browser leaves a gap above the shortcut footer.
	coverHeight := browserHeight - 1
	if cols, rows := m.coverSize(spectrumWidth, coverHeight); cols > 0 && rows > 0 {
		browser := lipgloss.NewStyle().Width(infoWidth).Height(browserHeight).Render(m.youtubeMusicBrowserView(infoWidth, browserHeight))
		// The panel border puts content one row and one column in.
		m.placeCover(1+lipgloss.Height(top)+2, 1+infoWidth+gap, spectrumWidth, coverHeight, cols, rows, browser)
		return top + "\n\n" + lipgloss.JoinHorizontal(lipgloss.Top, browser, strings.Repeat(" ", gap), coverPlaceholder(spectrumWidth, coverHeight))
	}
	browser := m.youtubeMusicBrowserView(innerWidth, browserHeight)
	return top + "\n\n" + browser
}

func (m Model) youtubeMusicBrowserView(innerWidth, height int) string {
	muted := lipgloss.NewStyle().Foreground(m.colors.Muted)
	if m.youtubeMusicURLResolving {
		return lipgloss.NewStyle().Foreground(m.colors.Yellow).Bold(true).Render("Reading song from URL…")
	}
	if m.youtubeMusicSearchOpen {
		return m.youtubeMusicSearchView(innerWidth, height)
	}
	if m.youtubeMusicLoading {
		message := "Loading YouTube Music library…"
		if len(m.youtubeMusicPlaylists) > 0 {
			message = "Loading playlist and continuation pages…"
		}
		return lipgloss.NewStyle().Foreground(m.colors.Yellow).Bold(true).Render(message)
	}
	if m.youtubeMusicErr != nil && len(m.youtubeMusicPlaylists) == 0 {
		return lipgloss.NewStyle().Foreground(m.colors.Red).Bold(true).Render("YouTube Music unavailable") + "\n" +
			muted.Render(truncate(m.youtubeMusicErr.Error(), innerWidth)) + "\n" +
			muted.Render("Press Ctrl+R to retry, or Ctrl+P to choose your browser in setup.")
	}

	account := "MUSIC LIBRARY"
	if m.youtubeMusicAccount.Name != "" {
		account += "  ·  " + m.youtubeMusicAccount.Name
	}
	header := lipgloss.NewStyle().Foreground(m.colors.Cyan).Bold(true).Render(truncate(account, innerWidth))
	if m.youtubeMusicView == youtubeMusicPlaylistView {
		return m.youtubeMusicPlaylistViewContent(header, innerWidth, height)
	}
	status := ""
	if m.youtubeMusicDeleteStatus != "" {
		color := m.colors.Green
		if m.youtubeMusicDeleteStatusError {
			color = m.colors.Red
		}
		status = lipgloss.NewStyle().Foreground(color).Render(truncate(m.youtubeMusicDeleteStatus, innerWidth))
	}
	if len(m.youtubeMusicPlaylists) == 0 {
		lines := []string{header}
		if status != "" {
			lines = append(lines, status)
		}
		return strings.Join(append(lines, "", muted.Render("No playlists found in this account.")), "\n")
	}

	lines := []string{header, muted.Render(fmt.Sprintf("%d playlists", len(m.youtubeMusicPlaylists))), ""}
	if status != "" {
		lines = append(lines, status)
	}
	selectedIndex := min(max(0, m.youtubeMusicPlaylistSelected), len(m.youtubeMusicPlaylists)-1)
	visibleRows := max(1, height-lipgloss.Height(strings.Join(lines, "\n"))-1)
	start, end := m.selectionWindow(playlistScroll, len(m.youtubeMusicPlaylists), selectedIndex, visibleRows)
	for index := start; index < end; index++ {
		playlist := m.youtubeMusicPlaylists[index]
		selected := index == selectedIndex
		cursor := "  "
		if playlist.ID == m.youtubeMusicQueue.ID {
			cursor = lipgloss.NewStyle().Foreground(m.colors.Green).Bold(true).Render("▶ ")
		} else if selected {
			cursor = lipgloss.NewStyle().Foreground(m.colors.Magenta).Bold(true).Render("> ")
		}
		details := ""
		if searchPlaylist(playlist) {
			details = "  SEARCH"
		}
		if playlist.TrackCount > 0 {
			details += fmt.Sprintf("  %d tracks", playlist.TrackCount)
		}
		available := max(8, innerWidth-2-lipgloss.Width(details))
		title := lipgloss.NewStyle().Foreground(m.colors.Foreground).Bold(selected).Underline(selected).Render(truncate(playlist.Title, available))
		lines = append(lines, cursor+title+muted.Render(details))
	}
	return strings.Join(lines, "\n")
}

func (m Model) youtubeMusicPlaylistViewContent(header string, innerWidth, height int) string {
	muted := lipgloss.NewStyle().Foreground(m.colors.Muted)
	playlist := m.youtubeMusicPlaylist
	title := playlist.Title
	if title == "" {
		title = "Untitled playlist"
	}
	origin := "YOUTUBE"
	if searchPlaylist(playlist) {
		origin = "SEARCH"
	}
	lines := []string{
		header,
		lipgloss.NewStyle().Foreground(m.colors.Magenta).Bold(true).Render(truncate(title, innerWidth)),
		muted.Render(fmt.Sprintf("%s  ·  %d tracks", origin, len(playlist.Tracks))),
	}
	if m.youtubeMusicStoreErr != nil {
		lines = append(lines, lipgloss.NewStyle().Foreground(m.colors.Red).Render(
			truncate("MUSIC DB  "+m.youtubeMusicStoreErr.Error(), innerWidth),
		))
	}
	if m.youtubeMusicDeleteStatus != "" {
		color := m.colors.Green
		if m.youtubeMusicDeleteStatusError {
			color = m.colors.Red
		}
		lines = append(lines, lipgloss.NewStyle().Foreground(color).Render(truncate(m.youtubeMusicDeleteStatus, innerWidth)))
	}
	if status := m.youtubeMusicCacheStatusView(innerWidth); status != "" {
		lines = append(lines, status)
	}
	lines = append(lines, "")
	if len(playlist.Tracks) == 0 {
		return strings.Join(append(lines, muted.Render("This playlist has no available tracks.")), "\n")
	}
	selectedIndex := min(max(0, m.youtubeMusicTrackSelected), len(playlist.Tracks)-1)
	queued := m.browsingQueue()
	visibleRows := max(1, height-lipgloss.Height(strings.Join(lines, "\n"))-3)
	start, end := m.selectionWindow(trackScroll, len(playlist.Tracks), selectedIndex, visibleRows)
	for index := start; index < end; index++ {
		track := playlist.Tracks[index]
		selected := index == selectedIndex
		cursor := "  "
		if queued && index == m.youtubeMusicPlayingIndex {
			symbol := "▶ "
			color := m.colors.Green
			if m.youtubeMusicPlayback.State == playback.StatePaused {
				symbol = "Ⅱ "
				color = m.colors.Yellow
			}
			cursor = lipgloss.NewStyle().Foreground(color).Bold(true).Render(symbol)
		} else if selected {
			cursor = lipgloss.NewStyle().Foreground(m.colors.Magenta).Bold(true).Render("> ")
		}
		artist := trackArtistNames(track.Artists)
		duration := ""
		if track.Duration > 0 {
			duration = formatMediaTime(track.Duration)
		}
		details := artist
		if duration != "" {
			if details != "" {
				details += "  ·  "
			}
			details += duration
		}
		if count := m.youtubeMusicPlayCounts[track.VideoID]; count > 0 {
			if details != "" {
				details += "  ·  "
			}
			label := "PLAY"
			if count != 1 {
				label = "PLAYS"
			}
			details += fmt.Sprintf("%d %s", count, label)
		}
		if m.youtubeMusicCached[track.VideoID] != "" {
			if details != "" {
				details += "  ·  "
			}
			details += "LOCAL"
		}
		if queued && index == m.youtubeMusicLoopTrackIndex {
			if details != "" {
				details += "  ·  "
			}
			details += "LOOP"
		}
		if details != "" {
			details = "  " + details
		}
		available := max(8, innerWidth-2-lipgloss.Width(details))
		titleColor := m.colors.Foreground
		if !track.Available {
			titleColor = m.colors.Muted
		}
		trackTitle := lipgloss.NewStyle().Foreground(titleColor).Bold(selected).Underline(selected).Render(truncate(track.Title, available))
		lines = append(lines, cursor+trackTitle+muted.Render(details))
	}
	return strings.Join(lines, "\n")
}

func (m Model) youtubeMusicCacheStatusView(width int) string {
	status := m.youtubeMusicCacheStatus
	if status.phase == "" {
		if m.youtubeMusicCacheErr != nil {
			return lipgloss.NewStyle().Foreground(m.colors.Red).Render(truncate("CACHE  "+m.youtubeMusicCacheErr.Error(), width))
		}
		return ""
	}
	slot := "NEXT"
	if m.youtubeMusicCacheStatusNow {
		slot = "NOW"
	}
	track := ""
	if status.trackIndex >= 0 && status.trackIndex < len(m.youtubeMusicQueue.Tracks) {
		track = m.youtubeMusicQueue.Tracks[status.trackIndex].Title
	}
	label := strings.ToUpper(string(status.phase))
	color := m.colors.Yellow
	if status.phase == musicCacheReady {
		color = m.colors.Green
	} else if status.phase == musicCacheFailed {
		color = m.colors.Red
	}
	progress := ""
	if status.phase == musicCacheDownloading && status.totalBytes > 0 {
		percent := int(status.downloaded * 100 / status.totalBytes)
		barWidth := min(12, max(4, width/5))
		filled := min(barWidth, percent*barWidth/100)
		progress = " [" + strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled) + fmt.Sprintf("] %d%%", percent)
	}
	if status.err != nil {
		track = status.err.Error()
	}
	text := "DOWNLOAD  " + slot + "  " + label + progress
	if track != "" {
		text += "  " + track
	}
	return lipgloss.NewStyle().Foreground(color).Render(truncate(text, width))
}

func (m Model) youtubeMusicInfoView(width int) string {
	muted := lipgloss.NewStyle().Foreground(m.colors.Muted)
	if m.youtubeMusicResolving && (m.youtubeMusicPlayer == nil || m.youtubeMusicPlayingIndex < 0) {
		return lipgloss.NewStyle().Foreground(m.colors.Yellow).Bold(true).Render(truncate("Preparing selected track…", width))
	}
	if m.youtubeMusicPlaybackErr != nil && (m.youtubeMusicPlayer == nil || m.youtubeMusicPlayingIndex < 0) {
		return lipgloss.NewStyle().Foreground(m.colors.Red).Bold(true).Render(
			truncate("Playback failed: "+m.youtubeMusicPlaybackErr.Error(), width),
		)
	}
	if m.youtubeMusicPlayer == nil || m.youtubeMusicPlayingIndex < 0 {
		return muted.Render("Nothing is playing.")
	}

	snapshot := m.youtubeMusicPlayback
	stateColor := m.colors.Yellow
	if snapshot.State == playback.StatePlaying {
		stateColor = m.colors.Green
	}
	stateText := strings.ToUpper(string(snapshot.State))
	if m.youtubeMusicResolving {
		stateText = "PREPARING NEXT"
	}
	if m.youtubeMusicPlaybackErr != nil {
		stateText = "ERROR"
		stateColor = m.colors.Red
	}
	state := lipgloss.NewStyle().Foreground(stateColor).Bold(true).Render(stateText)
	volume := fmt.Sprintf("VOL %d%%", snapshot.Volume)
	if snapshot.Muted {
		volume = "MUTED"
	}
	loopMode := "LOOP ALL"
	if m.youtubeMusicLoopTrackIndex >= 0 {
		loopMode = "LOOP 1"
	}
	volume += " · " + loopMode
	if m.youtubeMusicShuffle {
		volume += " · SHUFFLE"
	}
	artist := snapshot.Source.Artist
	if artist == "" {
		artist = "Unknown artist"
	}
	timeline := formatMediaTime(snapshot.Position) + " / " + formatMediaTime(snapshot.Duration)
	return strings.Join([]string{
		lipgloss.NewStyle().Foreground(m.colors.Magenta).Bold(true).Render(truncate(snapshot.Source.Title, width)),
		lipgloss.NewStyle().Foreground(m.colors.Cyan).Render(truncate(artist, width)),
		spread(state, muted.Render(volume), width),
		"",
		m.progressBar(snapshot.Position, snapshot.Duration, width),
		muted.Render(timeline),
	}, "\n")
}

func (m Model) youtubeMusicSpectrumPanel(width, height int) string {
	if m.youtubeMusicPlayer == nil || m.youtubeMusicPlayingIndex < 0 {
		return lipgloss.NewStyle().Foreground(m.colors.Muted).Render("Waiting for playback…")
	}
	return m.musicSpectrumView(width, height)
}

func (m Model) musicSpectrumView(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if m.spectrumAnalyzer == nil {
		return ""
	}
	snapshot := m.spectrumAnalyzer.Snapshot(m.youtubeMusicPlayback.Latency)
	if len(snapshot.Bands) == 0 {
		return ""
	}
	rows := brailleSpectrum(snapshot.Bands, width, height)
	for row := range rows {
		rows[row] = lipgloss.NewStyle().Foreground(spectrumRowColor(m.colors, row, len(rows))).Render(rows[row])
	}
	return strings.Join(rows, "\n")
}

func spectrumRowColor(colors theme.Theme, row, height int) lipgloss.Color {
	if height <= 0 {
		return colors.Cyan
	}
	level := float64(height-row) / float64(height)
	switch {
	case level > 0.75:
		return colors.Magenta
	case level > 0.50:
		return colors.Yellow
	case level > 0.25:
		return colors.Green
	default:
		return colors.Cyan
	}
}

func brailleSpectrum(bands []float64, width, height int) []string {
	if len(bands) == 0 || width <= 0 || height <= 0 {
		return nil
	}
	const brailleBase = rune(0x2800)
	leftDots := [4]rune{0x01, 0x02, 0x04, 0x40}
	rightDots := [4]rune{0x08, 0x10, 0x20, 0x80}
	totalPixels := height * 4
	subcolumns := width * 2
	levels := make([]int, subcolumns)
	for column := range levels {
		position := 0.0
		if subcolumns > 1 {
			position = float64(column) * float64(len(bands)-1) / float64(subcolumns-1)
		}
		left := min(len(bands)-1, int(position))
		right := min(len(bands)-1, left+1)
		fraction := position - float64(left)
		level := bands[left]*(1-fraction) + bands[right]*fraction
		levels[column] = min(totalPixels, max(0, int(math.Round(level*float64(totalPixels)))))
	}

	rows := make([]string, height)
	for row := 0; row < height; row++ {
		var line strings.Builder
		line.Grow(width * 3)
		for cell := 0; cell < width; cell++ {
			bits := rune(0)
			for pixel := 0; pixel < 4; pixel++ {
				globalPixel := row*4 + pixel
				if globalPixel >= totalPixels-levels[cell*2] {
					bits |= leftDots[pixel]
				}
				if globalPixel >= totalPixels-levels[cell*2+1] {
					bits |= rightDots[pixel]
				}
			}
			line.WriteRune(brailleBase + bits)
		}
		rows[row] = line.String()
	}
	return rows
}

func (m Model) selectionWindow(scrollID string, total, selected, visible int) (int, int) {
	if total <= 0 || visible <= 0 {
		return 0, 0
	}
	selected = min(max(0, selected), total-1)
	maximumStart := max(0, total-visible)
	start := 0
	if m.listScroll != nil {
		start = min(max(0, m.listScroll[scrollID]), maximumStart)
	}
	end := min(total, start+visible)
	if visible <= 2 {
		if selected < start {
			start = selected
		} else if selected >= end {
			start = selected - visible + 1
		}
	} else if selected < start+1 {
		start = selected - 1
	} else if selected > end-2 {
		start = selected - visible + 2
	}
	start = min(max(0, start), maximumStart)
	end = min(total, start+visible)
	if m.listScroll != nil {
		m.listScroll[scrollID] = start
	}
	return start, end
}

func trackArtistNames(artists []youtubemusic.Artist) string {
	names := make([]string, 0, len(artists))
	for _, artist := range artists {
		if strings.TrimSpace(artist.Name) != "" {
			names = append(names, artist.Name)
		}
	}
	return strings.Join(names, ", ")
}

func (m Model) progressBar(position, duration time.Duration, width int) string {
	if width <= 0 {
		return ""
	}
	filled := 0
	if duration > 0 {
		filled = int(float64(position) / float64(duration) * float64(width))
		filled = min(width, max(0, filled))
	}
	active := lipgloss.NewStyle().Foreground(m.colors.Green).Render(strings.Repeat("━", filled))
	remaining := lipgloss.NewStyle().Foreground(m.colors.Muted).Render(strings.Repeat("─", width-filled))
	return active + remaining
}

func formatBytes(size int64) string {
	if size < 1024 {
		return fmt.Sprintf("%d B", size)
	}
	value, unit := float64(size)/1024, "KB"
	for _, next := range []string{"MB", "GB", "TB"} {
		if value < 1024 {
			break
		}
		value, unit = value/1024, next
	}
	return fmt.Sprintf("%.1f %s", value, unit)
}

func formatMediaTime(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	totalSeconds := int(duration.Round(time.Second).Seconds())
	return fmt.Sprintf("%d:%02d", totalSeconds/60, totalSeconds%60)
}
