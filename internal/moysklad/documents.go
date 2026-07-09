package moysklad

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// DocumentType enumerates the MoySklad document endpoints this client supports.
// The value is the entity path segment (e.g. "demand" -> /entity/demand).
type DocumentType string

const (
	DocDemand         DocumentType = "demand"         // отгрузка (продажа)
	DocCustomerOrder  DocumentType = "customerorder"  // заказ покупателя
	DocSupply         DocumentType = "supply"         // приёмка (закупка)
	DocPurchaseOrder  DocumentType = "purchaseorder"  // заказ поставщику
	DocInvoiceOut     DocumentType = "invoiceout"     // счёт покупателю
	DocInvoiceIn      DocumentType = "invoicein"      // счёт поставщика
	DocSalesReturn    DocumentType = "salesreturn"    // возврат покупателя
	DocPurchaseReturn DocumentType = "purchasereturn" // возврат поставщику
	DocPaymentIn      DocumentType = "paymentin"      // входящий платёж
	DocPaymentOut     DocumentType = "paymentout"     // исходящий платёж
)

// ValidDocumentType reports whether s is a supported document type.
func ValidDocumentType(s string) bool {
	switch DocumentType(s) {
	case DocDemand, DocCustomerOrder, DocSupply, DocPurchaseOrder,
		DocInvoiceOut, DocInvoiceIn, DocSalesReturn, DocPurchaseReturn,
		DocPaymentIn, DocPaymentOut:
		return true
	}
	return false
}

// Document is a trimmed view over the common fields of MoySklad documents.
// Monetary fields are in kopecks. Fields absent on a given document type simply
// stay zero/empty.
type Document struct {
	Meta                  Meta                    `json:"meta"`
	ID                    string                  `json:"id"`
	Name                  string                  `json:"name"`
	Moment                string                  `json:"moment"`
	Applicable            bool                    `json:"applicable"`
	Description           string                  `json:"description,omitempty"`
	Sum                   Amount                  `json:"sum"`
	VatSum                Amount                  `json:"vatSum,omitempty"`
	PayedSum              Amount                  `json:"payedSum,omitempty"`
	ShippedSum            Amount                  `json:"shippedSum,omitempty"`
	InvoicedSum           Amount                  `json:"invoicedSum,omitempty"`
	PaymentPlannedMoment  string                  `json:"paymentPlannedMoment,omitempty"`
	DeliveryPlannedMoment string                  `json:"deliveryPlannedMoment,omitempty"`
	Agent                 *NamedRef               `json:"agent,omitempty"` // counterparty
	Organization          *NamedRef               `json:"organization,omitempty"`
	Store                 *NamedRef               `json:"store,omitempty"`
	SalesChannel          *NamedRef               `json:"salesChannel,omitempty"`
	State                 *NamedRef               `json:"state,omitempty"`
	Positions             *ListResponse[Position] `json:"positions,omitempty"`
}

// Position is a document line. Price and Sum are in kopecks; Discount is a
// percentage.
type Position struct {
	ID         string   `json:"id"`
	Quantity   float64  `json:"quantity"`
	Price      Amount   `json:"price"`
	Discount   float64  `json:"discount"`
	Vat        int      `json:"vat"`
	Assortment NamedRef `json:"assortment"`
}

// DocumentQuery filters a document search.
type DocumentQuery struct {
	From           string   // moment >=
	To             string   // moment <=
	CounterpartyID string   // agent filter by id (href built internally)
	StateName      string   // filter by state.name (client-side is simpler; see note)
	Filter         []string // extra raw filters
	Search         string
	Expand         []string
	Limit          int
	Order          string
}

func (q DocumentQuery) values(baseURL string) url.Values {
	v := url.Values{}
	var filters []string
	if m := normalizeMoment(q.From); m != "" {
		filters = append(filters, "moment>="+m)
	}
	if m := normalizeMoment(q.To); m != "" {
		filters = append(filters, "moment<="+m)
	}
	if q.CounterpartyID != "" {
		filters = append(filters, "agent="+baseURL+"/entity/counterparty/"+q.CounterpartyID)
	}
	filters = append(filters, q.Filter...)
	for _, f := range filters {
		v.Add("filter", f)
	}
	if q.Search != "" {
		v.Set("search", q.Search)
	}
	if len(q.Expand) > 0 {
		v.Set("expand", joinComma(q.Expand))
	}
	if q.Order != "" {
		v.Set("order", q.Order)
	}
	return v
}

// SearchDocuments lists documents of a given type with filters and pagination.
func (c *Client) SearchDocuments(ctx context.Context, docType DocumentType, q DocumentQuery) ([]Document, error) {
	if !ValidDocumentType(string(docType)) {
		return nil, fmt.Errorf("moysklad: unsupported document type %q", docType)
	}
	path := "/entity/" + string(docType)
	base := q.values(c.baseURL)

	pageSize := c.pageLimit
	if pageSize <= 0 {
		pageSize = defaultPageLimit
	}
	if q.Limit > 0 && q.Limit < pageSize {
		pageSize = q.Limit
	}

	var all []Document
	offset := 0
	for {
		v := cloneValues(base)
		v.Set("limit", strconv.Itoa(pageSize))
		v.Set("offset", strconv.Itoa(offset))

		var page ListResponse[Document]
		if err := c.get(ctx, path, v, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Rows...)
		if len(page.Rows) < pageSize {
			break
		}
		if q.Limit > 0 && len(all) >= q.Limit {
			all = all[:q.Limit]
			break
		}
		offset += pageSize
		if page.Meta.Size > 0 && offset >= page.Meta.Size {
			break
		}
	}
	return all, nil
}

// GetDocument fetches one document by id, expanding positions and common refs.
func (c *Client) GetDocument(ctx context.Context, docType DocumentType, id string, expand []string) (*Document, error) {
	if !ValidDocumentType(string(docType)) {
		return nil, fmt.Errorf("moysklad: unsupported document type %q", docType)
	}
	v := url.Values{}
	if len(expand) > 0 {
		v.Set("expand", joinComma(expand))
	}
	// Escape the caller-supplied id so it can't inject extra path segments or a
	// query string into the request URL.
	var d Document
	if err := c.get(ctx, "/entity/"+string(docType)+"/"+url.PathEscape(id), v, &d); err != nil {
		return nil, err
	}
	return &d, nil
}
