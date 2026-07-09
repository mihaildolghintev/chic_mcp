package moysklad

import (
	"context"
	"net/url"
	"strconv"
)

// ListOptions are the common query parameters for collection endpoints.
type ListOptions struct {
	// Search is a full-text search term (MoySklad's `search` parameter).
	Search string
	// Filter holds raw MoySklad filter expressions, e.g. "archived=false".
	Filter []string
	// Expand lists nested entities to inline, e.g. "positions.assortment".
	Expand []string
	// Limit caps the number of rows fetched in total. 0 means "all pages".
	Limit int
	// Order is a MoySklad sort expression, e.g. "name,asc".
	Order string
}

func (o ListOptions) values() url.Values {
	v := url.Values{}
	if o.Search != "" {
		v.Set("search", o.Search)
	}
	for _, f := range o.Filter {
		v.Add("filter", f)
	}
	if len(o.Expand) > 0 {
		v.Set("expand", joinComma(o.Expand))
	}
	if o.Order != "" {
		v.Set("order", o.Order)
	}
	return v
}

func joinComma(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}

// getAll fetches every page of a collection endpoint, following offset/limit
// pagination until MoySklad reports no more rows (or opts.Limit is reached).
func getAll[T any](ctx context.Context, c *Client, path string, opts ListOptions) ([]T, error) {
	base := opts.values()
	pageSize := c.pageLimit
	if pageSize <= 0 {
		pageSize = defaultPageLimit
	}
	if opts.Limit > 0 && opts.Limit < pageSize {
		pageSize = opts.Limit
	}

	var all []T
	offset := 0
	for {
		q := cloneValues(base)
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("offset", strconv.Itoa(offset))

		var page ListResponse[T]
		if err := c.get(ctx, path, q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Rows...)

		// Stop when this page was short (last page) or we've hit the cap.
		if len(page.Rows) < pageSize {
			break
		}
		if opts.Limit > 0 && len(all) >= opts.Limit {
			all = all[:opts.Limit]
			break
		}
		offset += pageSize
		// Guard against an endpoint that keeps returning full pages past size.
		if page.Meta.Size > 0 && offset >= page.Meta.Size {
			break
		}
	}
	return all, nil
}

func cloneValues(v url.Values) url.Values {
	out := make(url.Values, len(v))
	for k, vs := range v {
		cp := make([]string, len(vs))
		copy(cp, vs)
		out[k] = cp
	}
	return out
}
