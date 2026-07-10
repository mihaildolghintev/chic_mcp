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
	DocMove           DocumentType = "move"           // перемещение между складами
	DocInventory      DocumentType = "inventory"      // инвентаризация
	DocLoss           DocumentType = "loss"           // списание
	DocEnter          DocumentType = "enter"          // оприходование
	DocProcessing     DocumentType = "processing"     // техоперация
)

// docTypeInfo is the single source of truth for every supported document type:
// its order drives the enum shown to clients, and hasCurrency gates the
// rate.currency expand. Adding a type is one line here — ValidDocumentType,
// HasCurrency and DocumentTypeStrings all derive from it.
type docTypeInfo struct {
	Type        DocumentType
	HasCurrency bool // commercial docs carry a currency; warehouse ops do not
}

var documentTypes = []docTypeInfo{
	{DocDemand, true},
	{DocCustomerOrder, true},
	{DocSupply, true},
	{DocPurchaseOrder, true},
	{DocInvoiceOut, true},
	{DocInvoiceIn, true},
	{DocSalesReturn, true},
	{DocPurchaseReturn, true},
	{DocPaymentIn, true},
	{DocPaymentOut, true},
	{DocMove, false},
	{DocInventory, false},
	{DocLoss, false},
	{DocEnter, false},
	{DocProcessing, false},
}

// HasCurrency reports whether a document type carries a currency/rate. Only the
// commercial documents do; the warehouse operations (move, enter, loss,
// inventory, processing) are always in the base currency and reject a
// rate.currency expand, so callers must not request it for them.
func HasCurrency(t DocumentType) bool {
	for _, d := range documentTypes {
		if d.Type == t {
			return d.HasCurrency
		}
	}
	return false
}

// ValidDocumentType reports whether s is a supported document type.
func ValidDocumentType(s string) bool {
	for _, d := range documentTypes {
		if string(d.Type) == s {
			return true
		}
	}
	return false
}

// DocumentTypeStrings returns every supported document type in display order,
// for building a JSON-schema enum.
func DocumentTypeStrings() []string {
	out := make([]string, len(documentTypes))
	for i, d := range documentTypes {
		out[i] = string(d.Type)
	}
	return out
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
	Rate                  *Rate                   `json:"rate,omitempty"`
	Positions             *ListResponse[Position] `json:"positions,omitempty"`
}

// Rate is a document's currency and exchange rate. Sum/payedSum and position
// prices are stored in the DOCUMENT's currency minor units, not the account's
// base currency — Currency (once expanded via rate.currency) is what labels
// them honestly. Value is МойСклад's raw rate figure; base-currency conversion
// is intentionally left to the caller because its direction depends on the
// currency's indirect flag.
type Rate struct {
	Value    float64   `json:"value,omitempty"`
	Currency *Currency `json:"currency,omitempty"`
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
	if m := normalizeMomentEnd(q.To); m != "" {
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
