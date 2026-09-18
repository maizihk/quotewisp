package httpmw

type ctxKey uint8

const (
	requestIDKey ctxKey = iota
	clientIPKey
	routeKey
)
