package moysklad

import "context"

// ListProducts fetches products from entity/product, following pagination.
func (c *Client) ListProducts(ctx context.Context, opts ListOptions) ([]Product, error) {
	return getAll[Product](ctx, c, "/entity/product", opts)
}
