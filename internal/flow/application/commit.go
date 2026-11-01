package application

import (
	accessapi "organon/internal/access/api"
	contentapi "organon/internal/content/api"
	"organon/internal/kernel"
)

func commitReq(p accessapi.Principal, action string, id kernel.ID, typeName string, v kernel.Version, data map[string]any) contentapi.CommitRequest {
	return contentapi.CommitRequest{Principal: p, Action: action, Ops: []contentapi.WriteOp{
		{Op: "update", Type: typeName, ID: id.String(), Version: v, Data: data},
	}}
}
