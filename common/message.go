package common

import (
	"github.com/NimoTech/NimoOS/codegen/message_bus"
)

// devtype -> action -> event
var EventTypes = []message_bus.EventType{
	{Name: "nimoos:system:utilization", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "nimoos:file:recover", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "nimoos:file:operate", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "nimoos:media:deleted", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
}
