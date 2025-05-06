package contracts

import (
	"errors"
	"fmt"
)

type ContextTags map[string]string

var tagMaxLengths = map[string]int{
	"ai.application.ver":             1024,
	"ai.device.id":                   1024,
	"ai.device.locale":               64,
	"ai.device.model":                256,
	"ai.device.oemName":              256,
	"ai.device.osVersion":            256,
	"ai.device.type":                 64,
	"ai.location.ip":                 46,
	"ai.operation.id":                128,
	"ai.operation.name":              1024,
	"ai.operation.parentId":          128,
	"ai.operation.syntheticSource":   1024,
	"ai.operation.correlationVector": 64,
	"ai.session.id":                  64,
	"ai.session.isFirst":             5,
	"ai.user.accountId":              1024,
	"ai.user.id":                     128,
	"ai.user.authUserId":             1024,
	"ai.cloud.role":                  256,
	"ai.cloud.roleInstance":          256,
	"ai.internal.sdkVersion":         64,
	"ai.internal.agentVersion":       64,
	"ai.internal.nodeName":           256,
}

// Truncates tag values that exceed their maximum supported lengths.  Returns
// warnings for each affected field.
func SanitizeTags(tags map[string]string) error {
	var errs []error
	for k, v := range tags {
		if maxlen, ok := tagMaxLengths[k]; ok && len(v) > maxlen {
			tags[k] = v[:maxlen]
			errs = append(errs, fmt.Errorf("%s exceeded maximum length of %d", k, maxlen))
		}
	}
	return errors.Join(errs...)
}
