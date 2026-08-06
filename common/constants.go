package common

const (
	SERVICENAME = "nimoos"
	BODY        = " "
	RANW_NAME   = "Nimo-RemoteAccess"
	// NimoOS-Cookie global shared secret (compiled into firmware on-device, in a config file in the cloud)
	CookieSecret = "NimoOS-Cookie-Secret-2026"
)

// VERSION is a var (not const) so it can be overridden via -ldflags at build time.
var VERSION = "dev"
