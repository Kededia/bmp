package app

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-sixel"
	"golang.org/x/image/draw"

	"bmp/internal/youtubemusic"
)

// Windows Terminal draws Sixel images on a virtual 10 by 20 pixel cell,
// scaled to the real font, so sizes in cells convert to pixels this way.
const (
	sixelCellWidth  = 10
	sixelCellHeight = 20
)

// sixelSupported is set by DetectSixel. Windows Terminal draws Sixel images;
// the Windows 10 console does not, so it shows no cover.
var sixelSupported bool

// DetectSixel asks the terminal whether it draws Sixel images, before Bubble
// Tea takes over the console, and reports it.
func DetectSixel() bool {
	sixelSupported = terminalHasSixel()
	return sixelSupported
}

type coverMsg struct {
	url   string
	image image.Image
}

// coverRender keeps the encoded cover and what the screen around it showed
// when it was last drawn. Bubble Tea redraws only changed lines, and a
// redrawn line paints over the part of the cover on it, so the cover is drawn
// again after any change under it, and not on frames that only refresh the
// spectrum.
type coverRender struct {
	key        string
	sixel      string
	signature  string
	generation int
	// draw is the escape sequence the current view puts on its last line.
	draw string
}

// fetchCover reads the cover from the covers folder of the music folder,
// downloading and keeping it there the first time.
func fetchCover(parent context.Context, track youtubemusic.Track, musicDirectory string) tea.Cmd {
	url := youtubemusic.CoverURL(track)
	albumPath := filepath.Join(musicDirectory, "covers", track.VideoID+".jpg")
	path := albumPath
	if track.Thumbnail == "" {
		path = filepath.Join(musicDirectory, "covers", track.VideoID+"-video.jpg")
	}
	return func() tea.Msg {
		// Album art kept from an earlier play comes first, so a song resumed
		// before its playlist loads shows it rather than the video thumbnail.
		video := false
		data, err := os.ReadFile(albumPath)
		if err != nil && path != albumPath {
			video = true
			data, err = os.ReadFile(path)
		}
		if err != nil {
			if data, err = downloadCover(parent, url); err != nil {
				return coverMsg{url: url}
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
				_ = os.WriteFile(path, data, 0o644)
			}
		}
		decoded, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return coverMsg{url: url}
		}
		// The video thumbnail is 4:3 with black bars around a 16:9 picture.
		if cropper, ok := decoded.(interface {
			SubImage(image.Rectangle) image.Image
		}); ok && video {
			bounds := decoded.Bounds()
			bar := (bounds.Dy() - bounds.Dx()*9/16) / 2
			decoded = cropper.SubImage(image.Rect(bounds.Min.X, bounds.Min.Y+bar, bounds.Max.X, bounds.Max.Y-bar))
		}
		return coverMsg{url: url, image: decoded}
	}
}

func downloadCover(parent context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download cover: %s", response.Status)
	}
	return io.ReadAll(io.LimitReader(response.Body, 4<<20))
}

// coverCommand fetches the playing song's cover when it changed, as when a
// resumed song's playlist brings its album art. The previous cover stays up
// until the new one arrives.
func (m *Model) coverCommand() tea.Cmd {
	index := m.youtubeMusicPlayingIndex
	if !sixelSupported || m.youtubeMusicCache == nil || index < 0 || index >= len(m.youtubeMusicQueue.Tracks) {
		return nil
	}
	track := m.youtubeMusicQueue.Tracks[index]
	if track.VideoID == "" || youtubemusic.CoverURL(track) == m.coverURL {
		return nil
	}
	m.coverURL = youtubemusic.CoverURL(track)
	return fetchCover(m.appContext, track, m.youtubeMusicCache.Directory())
}

func (m *Model) handleCover(msg coverMsg) {
	if msg.url == m.coverURL {
		m.coverImage, m.coverImageURL = msg.image, msg.url
	}
}

// coverSize returns the cells the cover takes within width by height cells,
// keeping its proportions, or zero when there is no cover to show.
func (m Model) coverSize(width, height int) (cols, rows int) {
	if m.coverImage == nil || m.youtubeMusicPlayingIndex < 0 || width <= 0 || height <= 0 {
		return 0, 0
	}
	bounds := m.coverImage.Bounds()
	pixelWidth := width * sixelCellWidth
	pixelHeight := pixelWidth * bounds.Dy() / max(1, bounds.Dx())
	if pixelHeight > height*sixelCellHeight {
		pixelHeight = height * sixelCellHeight
		pixelWidth = pixelHeight * bounds.Dx() / max(1, bounds.Dy())
	}
	return pixelWidth / sixelCellWidth, pixelHeight / sixelCellHeight
}

// placeCover encodes the cols by rows cover, centered at the top of the width
// by height cells area at row and column of the screen, and sets the drawing
// the view carries. The image covers that whole area, filling around the
// cover with the theme background, so neither cells under it nor a larger
// previous cover show through. around is the text sharing the area's lines;
// when it changes, the drawing is numbered anew.
func (m Model) placeCover(row, column, width, height, cols, rows int, around string) {
	render := m.coverRender
	key := fmt.Sprintf("%s %dx%d %dx%d %s", m.coverImageURL, width, height, cols, rows, m.colors.Background)
	if render.key != key {
		cover := image.NewRGBA(image.Rect(0, 0, cols*sixelCellWidth, rows*sixelCellHeight))
		draw.CatmullRom.Scale(cover, cover.Bounds(), m.coverImage, m.coverImage.Bounds(), draw.Src, nil)
		// Sixel has at most 255 colors. The encoder picks them for the cover
		// alone, then the theme background joins them as its own color, so
		// the area around the cover matches the rest of the screen.
		var encoded bytes.Buffer
		if err := sixel.NewEncoder(&encoded).Encode(cover); err != nil {
			return
		}
		palette := append(sixelPalette(encoded.String(), 254), sixelBackground(string(m.colors.Background)))
		canvas := image.NewRGBA(image.Rect(0, 0, width*sixelCellWidth, height*sixelCellHeight))
		draw.Draw(canvas, canvas.Bounds(), image.NewUniform(hexColor(string(m.colors.Background))), image.Point{}, draw.Src)
		at := image.Pt((width-cols)/2*sixelCellWidth, (height-rows)/2*sixelCellHeight)
		draw.Draw(canvas, cover.Bounds().Add(at), cover, image.Point{}, draw.Src)
		encoded.Reset()
		encoder := sixel.NewEncoder(&encoded)
		encoder.Palette = palette
		if err := encoder.Encode(canvas); err != nil {
			return
		}
		render.key, render.sixel = key, encoded.String()
	}
	signature := fmt.Sprintf("%s %d %d\n%s", key, row, column, around)
	if signature != render.signature {
		render.signature = signature
		render.generation++
	}
	// An ignored APC string numbers each drawing, so the line changes, and
	// Bubble Tea writes it, exactly when the cover needs drawing again. Every
	// view carries the drawing, so it survives views Bubble Tea skips. The
	// theme background is set while drawing, as the terminal fills the
	// image's cells with the current background.
	background := lipgloss.ColorProfile().Color(string(m.colors.Background)).Sequence(true)
	render.draw = fmt.Sprintf("\x1b_bmp%d\x1b\\\x1b7\x1b[%d;%dH\x1b[%sm%s\x1b8", render.generation, row+1, column+1, background, render.sixel)
}

var sixelRegister = regexp.MustCompile(`#\d+;2;(\d+);(\d+);(\d+)`)

// sixelPalette reads up to limit color registers an encoded image defines.
func sixelPalette(encoded string, limit int) color.Palette {
	var palette color.Palette
	for _, match := range sixelRegister.FindAllStringSubmatch(encoded, limit) {
		r, _ := strconv.Atoi(match[1])
		g, _ := strconv.Atoi(match[2])
		b, _ := strconv.Atoi(match[3])
		palette = append(palette, sixelColor(r, g, b))
	}
	return palette
}

// sixelBackground is the theme background as close as Sixel's whole percents
// allow.
func sixelBackground(value string) color.Color {
	r, g, b, _ := hexColor(value).RGBA()
	percent := func(channel uint32) int { return int((channel*100 + 0xffff/2) / 0xffff) }
	return sixelColor(percent(r), percent(g), percent(b))
}

// sixelColor is the color the encoder writes as these percents. It truncates
// to whole percents, so each channel is rounded up onto them.
func sixelColor(r, g, b int) color.Color {
	channel := func(percent int) uint16 { return uint16(min(0xffff, (percent*0xffff+99)/100)) }
	return color.RGBA64{channel(r), channel(g), channel(b), 0xffff}
}

// hexColor parses a #rrggbb theme color.
func hexColor(value string) color.Color {
	var r, g, b uint8
	if _, err := fmt.Sscanf(value, "#%02x%02x%02x", &r, &g, &b); err != nil {
		return color.Black
	}
	return color.RGBA{r, g, b, 255}
}

// coverPlaceholder is the blank area the cover is drawn over.
func coverPlaceholder(cols, rows int) string {
	return strings.TrimSuffix(strings.Repeat(strings.Repeat(" ", cols)+"\n", rows), "\n")
}

// withCover puts the cover drawing at the start of the view's last line,
// which Bubble Tea writes after the lines above it.
func (m Model) withCover(view string) string {
	if m.coverRender == nil || m.coverRender.draw == "" {
		return view
	}
	last := strings.LastIndex(view, "\n") + 1
	return view[:last] + m.coverRender.draw + view[last:]
}
