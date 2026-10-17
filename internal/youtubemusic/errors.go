package youtubemusic

import "errors"

var (
	ErrNotConfigured       = errors.New("YouTube Music is not configured")
	ErrAuthentication      = errors.New("YouTube Music authentication failed")
	ErrSessionExpired      = errors.New("YouTube Music session expired")
	ErrUnavailable         = errors.New("YouTube Music content unavailable")
	ErrRateLimited         = errors.New("YouTube Music rate limited")
	ErrResolverUnavailable = errors.New("YouTube Music stream resolver unavailable")
)
