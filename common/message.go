package common

import (
	"github.com/NimoTech/NimoOS/codegen/message_bus"
)

// EventMediaCreated fires when a file finishes landing on disk (upload/copy/move).
// properties["paths"] is a JSON array whose elements can be file paths or
// directory paths (a whole-directory copy/move only emits the destination root).
const EventMediaCreated = "nimoos:media:created"

// devtype -> action -> event
var EventTypes = []message_bus.EventType{
	{Name: "nimoos:system:utilization", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "nimoos:file:recover", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "nimoos:file:operate", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "nimoos:media:deleted", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: EventMediaCreated, SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
}
