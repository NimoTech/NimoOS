package v2

import (
	"github.com/NimoTech/NimoOS/codegen"
)

type NimoOS struct{}

func NewNimoOS() codegen.ServerInterface {
	return &NimoOS{}
}
