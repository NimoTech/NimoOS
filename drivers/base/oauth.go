package base

// RedirectURI 是所有云盘 OAuth 授权回调统一使用的中转端点(cloudoauth 中转服务),
// 作为全部 driver 的单一真源。云盘控制台里登记的 redirect_uri 必须与此一致。
// 默认值为生产中转地址;如需在构建时切换,可用:
//
//	-ldflags "-X github.com/NimoTech/NimoOS/drivers/base.RedirectURI=https://<host>"
var RedirectURI = "https://cloudoauth.nimotech.ai"
