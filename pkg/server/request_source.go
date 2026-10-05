package server

// RequestSource separates user demand from internal classification and cache work.
// Background work may refresh valid routing state without creating user activity.
type RequestSource uint8

const (
	RequestSourceUnspecified RequestSource = iota
	RequestSourceUser
	RequestSourcePrewarm
	RequestSourceRefresh
)

// IsBackground reports whether the request must avoid user-activity side effects.
func (s RequestSource) IsBackground() bool {
	return s == RequestSourcePrewarm || s == RequestSourceRefresh
}

func (s RequestSource) String() string {
	switch s {
	case RequestSourceUser:
		return "user"
	case RequestSourcePrewarm:
		return "prewarm"
	case RequestSourceRefresh:
		return "refresh"
	default:
		return ""
	}
}
