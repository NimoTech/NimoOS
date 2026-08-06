package base

// RedirectURI is the shared relay endpoint (the cloudoauth relay service)
// used by all cloud-drive OAuth authorization callbacks, and is the single
// source of truth for every driver. The redirect_uri registered in each
// cloud-drive console must match this exactly. Defaults to the production
// relay address; to override it at build time, use:
//
//	-ldflags "-X github.com/NimoTech/NimoOS/drivers/base.RedirectURI=https://<host>"
var RedirectURI = "https://cloudoauth.nimotech.ai"
