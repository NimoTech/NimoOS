package v2

import (
	"github.com/NimoTech/NimoOS/codegen"
	"github.com/NimoTech/NimoOS/service"
)

type CasaOS struct {
	fileUploadService *service.FileUploadService
}

func NewCasaOS() codegen.ServerInterface {
	return &CasaOS{
		fileUploadService: service.NewFileUploadService(),
	}
}
