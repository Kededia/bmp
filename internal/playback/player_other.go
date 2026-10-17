//go:build !windows

package playback

func New() (Player, error) {
	return nil, ErrUnsupported
}
