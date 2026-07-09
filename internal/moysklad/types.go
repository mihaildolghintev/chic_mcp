package moysklad

import (
	"encoding/json"
	"strconv"
)

// Meta is the metadata object attached to every MoySklad entity. For nested
// entities MoySklad returns only Meta (an href) unless the field is expanded.
type Meta struct {
	Href         string `json:"href,omitempty"`
	MetadataHref string `json:"metadataHref,omitempty"`
	Type         string `json:"type,omitempty"`
	MediaType    string `json:"mediaType,omitempty"`
	// The following are only present on list-level meta.
	Size     int    `json:"size,omitempty"`
	Limit    int    `json:"limit,omitempty"`
	Offset   int    `json:"offset,omitempty"`
	NextHref string `json:"nextHref,omitempty"`
	PrevHref string `json:"previousHref,omitempty"`
}

// ListResponse is the envelope MoySklad wraps every collection endpoint in.
type ListResponse[T any] struct {
	Context json.RawMessage `json:"context,omitempty"`
	Meta    Meta            `json:"meta"`
	Rows    []T             `json:"rows"`
}

// APIError is the error envelope MoySklad returns on 4xx/5xx responses.
// The API returns {"errors":[{...}]}; we surface the first error plus status.
type APIError struct {
	StatusCode int           `json:"-"`
	Errors     []APIErrorRow `json:"errors"`
}

type APIErrorRow struct {
	Error        string `json:"error"`
	Code         int    `json:"code"`
	MoreInfo     string `json:"moreInfo,omitempty"`
	Parameter    string `json:"parameter,omitempty"`
	ErrorMessage string `json:"errorMessage,omitempty"`
}

func (e *APIError) Error() string {
	if len(e.Errors) > 0 {
		row := e.Errors[0]
		msg := row.Error
		if row.ErrorMessage != "" {
			msg = row.ErrorMessage
		}
		return "moysklad: status " + strconv.Itoa(e.StatusCode) + ": " + msg
	}
	return "moysklad: status " + strconv.Itoa(e.StatusCode)
}

// Product is a trimmed entity/product row. Prices are in kopecks in the API;
// callers must convert to rubles at the aggregation layer.
type Product struct {
	Meta        Meta        `json:"meta"`
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Code        string      `json:"code,omitempty"`
	Article     string      `json:"article,omitempty"`
	Description string      `json:"description,omitempty"`
	Archived    bool        `json:"archived,omitempty"`
	SalePrices  []SalePrice `json:"salePrices,omitempty"`
	BuyPrice    *BuyPrice   `json:"buyPrice,omitempty"`
}

type SalePrice struct {
	Value     float64 `json:"value"` // kopecks
	PriceType struct {
		Name string `json:"name"`
	} `json:"priceType"`
}

type BuyPrice struct {
	Value float64 `json:"value"` // kopecks
}
