package common

const (
	SERVICENAME = "nimoos"
	BODY        = " "
	RANW_NAME   = "Nimo-RemoteAccess"
	// NimoOS-Cookie 全局共享密钥（设备端编译在固件中，云端在配置文件中）
	CookieSecret = "NimoOS-Cookie-Secret-2026"
)

// VERSION is a var (not const) so it can be overridden via -ldflags at build time.
var VERSION = "dev"
