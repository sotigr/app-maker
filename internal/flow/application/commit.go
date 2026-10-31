package application

import (
	accessapi "app-maker/internal/access/api"
	contentapi "app-maker/internal/content/api"
	"app-maker/internal/kernel"
)

func commitReq(p accessapi.Principal, action string, id kernel.ID, typeName string, v kernel.Version, data map[string]any) contentapi.CommitRequest {
	return contentapi.CommitRequest{Principal: p, Action: action, Ops: []contentapi.WriteOp{
		{Op: "update", Type: typeName, ID: id.String(), Version: v, Data: data},
	}}
}
