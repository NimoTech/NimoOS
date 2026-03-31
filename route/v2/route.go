package v2

import (
	"github.com/NimoTech/NimoOS/codegen"
	"github.com/NimoTech/NimoOS/service"
)

type NimoOS struct {
	fileUploadService *service.FileUploadService
}

func NewNimoOS() codegen.ServerInterface {
	return &NimoOS{
		fileUploadService: service.NewFileUploadService(),
	}
}
