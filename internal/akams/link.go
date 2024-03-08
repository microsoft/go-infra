// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package akams

type CreateLinkRequest struct {
	ShortURL                    string `json:"shortUrl,omitempty"`
	TargetURL                   string `json:"targetUrl"`
	MobileURL                   string `json:"mobileUrl,omitempty"`
	CreatedBy                   string `json:"createdBy"`
	LastModifiedBy              string `json:"lastModifiedBy"`
	IsVanity                    bool   `json:"isVanity,omitempty"`
	IsAllowParam                bool   `json:"isAllowParam,omitempty"`
	IsTrackParam                bool   `json:"isTrackParam,omitempty"`
	Description                 string `json:"description,omitempty"`
	GroupOwner                  string `json:"groupOwner,omitempty"`
	Owners                      string `json:"owners"`
	Category                    string `json:"category,omitempty"`
	IsActive                    bool   `json:"isActive,omitempty"`
	BypassCvsCheck              bool   `json:"bypassCvsCheck,omitempty"`
	BypassCvsCheckJustification string `json:"bypassCvsCheckJustification,omitempty"`
}

func (c *CreateLinkRequest) ToUpdateLinkRequest() UpdateLinkRequest {
	return UpdateLinkRequest{
		ShortURL:       c.ShortURL,
		TargetURL:      c.TargetURL,
		MobileURL:      c.MobileURL,
		IsAllowParam:   c.IsAllowParam,
		IsTrackParam:   c.IsTrackParam,
		Description:    c.Description,
		GroupOwner:     c.GroupOwner,
		LastModifiedBy: c.LastModifiedBy,
		Owners:         c.Owners,
		Category:       c.Category,
		IsActive:       c.IsActive,
	}
}

type UpdateLinkRequest struct {
	ShortURL       string `json:"shortUrl,omitempty"`
	TargetURL      string `json:"targetUrl"`
	MobileURL      string `json:"mobileUrl,omitempty"`
	IsAllowParam   bool   `json:"isAllowParam,omitempty"`
	IsTrackParam   bool   `json:"isTrackParam,omitempty"`
	Description    string `json:"description,omitempty"`
	GroupOwner     string `json:"groupOwner,omitempty"`
	LastModifiedBy string `json:"lastModifiedBy"`
	Owners         string `json:"owners"`
	Category       string `json:"category,omitempty"`
	IsActive       bool   `json:"isActive,omitempty"`
}
