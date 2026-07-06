package common

import (
	"github.com/NimoTech/NimoOS/codegen/message_bus"
)

// EventMediaCreated 文件落盘完成(上传/复制/移动)。properties["paths"] 为 JSON 数组,
// 元素可以是文件路径,也可以是目录路径(整目录复制/移动时只发目的地根)。
const EventMediaCreated = "nimoos:media:created"

// devtype -> action -> event
var EventTypes = []message_bus.EventType{
	{Name: "nimoos:system:utilization", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "nimoos:file:recover", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "nimoos:file:operate", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: "nimoos:media:deleted", SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
	{Name: EventMediaCreated, SourceID: SERVICENAME, PropertyTypeList: []message_bus.PropertyType{}},
}
